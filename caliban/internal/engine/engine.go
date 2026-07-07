// Package engine is the core of caliban. It owns the conversation lifecycle:
// assembling model context per run (base prompt + persona + memory index +
// rolling summary + transcript tail), executing runs via per-run pkg/golem
// agents rebuilt from the store, persisting the result, and fanning stream
// events out to subscribed transports.
//
// The store is the only conversation state (spec D1): agents are built per run,
// executed once, and discarded, so restart-safety is free. Transports talk only
// to engine; tools never import engine — engine injects capabilities into them
// as small interfaces.
package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/tools"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
)

// mainConversationID is the legacy/default Telegram conversation. Other
// transports may use their own default conversations; scheduled work targets the
// current run's conversation when available and falls back to this id.
const mainConversationID = 1

const (
	defaultTailBudgetTokens = 48000
	// defaultKeepRecentTokens is how much of the most recent tail compaction
	// keeps verbatim; the older remainder is folded into the summary. Decoupled
	// from the budget so the verbatim window can be tuned independently of the
	// trigger threshold (it must stay below the budget — see New).
	defaultKeepRecentTokens = 24000
	defaultRunTimeout       = 10 * time.Minute
	// defaultMaxToolIterations bounds a user-facing run's tool-call rounds. The
	// cap is a runaway/cost guard; hitting it yields a clean forced reply (golem
	// switches to a final answer), so it can sit generously high.
	defaultMaxToolIterations = 40
)

// Config wires the engine's dependencies. Tools and the main model are built in
// main; the store and workspace are opened there too.
type Config struct {
	Store     *store.Store
	Workspace *workspace.Workspace
	Main      golem.Model // the main model handle
	// MainModelID is the model URI recorded on runs (golem.Model hides it).
	MainModelID string
	// Cheap is the secondary model used for compaction (M3). nil disables it.
	Cheap            golem.Model
	CheapModelID     string
	Tools            []golem.Tool
	SkillCatalog     string
	Tasks            *tasks.Queue // M2; nil disables scheduling
	TailBudgetTokens int          // default 48000
	// KeepRecentTokens is the verbatim recent-tail window kept on compaction
	// (default 24000). Clamped below TailBudgetTokens.
	KeepRecentTokens int
	// MaxToolIterations caps tool-call rounds per user-facing run (default 40).
	MaxToolIterations int
	Timezone          *time.Location
	Logger            llm.Logger
}

// Engine executes conversation runs and broadcasts their stream events.
type Engine struct {
	store        *store.Store
	workspace    *workspace.Workspace
	main         golem.Model
	modelID      string
	cheap        golem.Model
	cheapModelID string
	tools        []golem.Tool
	skillCatalog string
	tasks        *tasks.Queue
	tailTok      int
	keepTok      int
	maxToolIter  int
	loc          *time.Location
	log          llm.Logger
	// freeTimeConvID is the resolved int id of the free-time conversation (looked
	// up by its sentinel uuid at Start), or 0 when free-time is disabled. Set once
	// before any worker is spawned, then read by runProfile.
	freeTimeConvID int64

	mu          sync.Mutex
	workers     map[int64]chan struct{} // conversation id -> buffered-1 kick channel
	subscribers map[int]func(Event)
	nextSubID   int
	notifiers   []Notifier
}

// Event carries either one golem stream event tagged with its conversation and
// run, or a persisted transcript message appended outside a run (for example a
// fired reminder).
type Event struct {
	ConversationID int64
	RunID          int64
	Ev             golem.StreamEvent
	Message        *store.Message
}

// Notifier is implemented by transports that can push to the user outside the
// reply flow. The engine calls it with the target conversation id; transports
// ignore conversations they do not own.
type Notifier interface {
	Notify(ctx context.Context, conversationID int64, text string) error
}

// scheduledTurnNotifier is an optional notifier extension for transports that
// need a short attention signal after a scheduled agent turn completes. Telegram
// already receives the normal final reply via run events, so it deliberately
// does not implement this.
type scheduledTurnNotifier interface {
	NotifyScheduledTurn(ctx context.Context, conversationID int64, reply string) error
}

func New(cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("engine: store is required")
	}
	if cfg.Workspace == nil {
		return nil, fmt.Errorf("engine: workspace is required")
	}
	if cfg.Main == nil {
		return nil, fmt.Errorf("engine: main model is required")
	}
	tail := cfg.TailBudgetTokens
	if tail <= 0 {
		tail = defaultTailBudgetTokens
	}
	keep := cfg.KeepRecentTokens
	if keep <= 0 {
		keep = defaultKeepRecentTokens
	}
	if keep >= tail {
		// The verbatim window must stay below the trigger, or a compaction would
		// leave the tail at/above budget and re-fire on the next run. Halve it.
		keep = tail / 2
	}
	maxToolIter := cfg.MaxToolIterations
	if maxToolIter <= 0 {
		maxToolIter = defaultMaxToolIterations
	}
	loc := cfg.Timezone
	if loc == nil {
		loc = time.UTC
	}
	e := &Engine{
		store:        cfg.Store,
		workspace:    cfg.Workspace,
		main:         cfg.Main,
		modelID:      cfg.MainModelID,
		cheap:        cfg.Cheap,
		cheapModelID: cfg.CheapModelID,
		tools:        cfg.Tools,
		skillCatalog: cfg.SkillCatalog,
		tasks:        cfg.Tasks,
		tailTok:      tail,
		keepTok:      keep,
		maxToolIter:  maxToolIter,
		loc:          loc,
		log:          cfg.Logger,
		workers:      make(map[int64]chan struct{}),
		subscribers:  make(map[int]func(Event)),
	}
	// The main conversation row must exist before anything appends to it — a
	// reminder firing or a scheduled turn can run before Start does. Make it a
	// construction invariant (like the worker lane below), not a Start-time one;
	// the store now rejects appends to a missing conversation.
	if _, err := e.store.EnsureMainConversation(context.Background()); err != nil {
		return nil, fmt.Errorf("engine: ensure main conversation: %w", err)
	}
	// The main conversation's worker lane exists for the engine's whole life, so
	// SubmitUserMessage never races a not-yet-started worker at boot. Start spawns
	// the goroutine that drains this channel; a kick before then just buffers.
	e.ensureWorker(mainConversationID)
	// The engine is itself the scheduling/notify capability the agent's tools
	// call back into. It registers them on itself, so tools never import engine
	// and main only builds engine-independent tools (shell). Scheduling tools
	// need the queue.
	if cfg.Tasks != nil {
		e.tools = append(e.tools, tools.Scheduling(e, loc)...)
		e.tools = append(e.tools, tools.Notify(e))
	}
	e.tools = append(e.tools, tools.History(e)...)
	e.tools = append(e.tools, tools.Delegation(e)...)
	return e, nil
}

// Start runs the engine until ctx is done. Callers that expose inbound transports
// should use StartReady so they can wait until worker lanes are running before
// accepting messages.
func (e *Engine) Start(ctx context.Context) error {
	return e.StartReady(ctx, nil)
}

// StartReady cleans up runs interrupted by a restart, ensures the main
// conversation exists, spawns a worker per active conversation, kicks each once
// (self-healing the "user wrote, process died before replying" case), closes
// ready when inbound message submission is safe, and blocks until ctx is done.
func (e *Engine) StartReady(ctx context.Context, ready chan<- struct{}) error {
	if n, err := e.store.FailRunningRuns(ctx); err != nil {
		return fmt.Errorf("engine: fail interrupted runs: %w", err)
	} else if n > 0 {
		e.logf(infoLevel, "marked %d interrupted run(s) as failed", n)
	}

	if _, err := e.store.EnsureMainConversation(ctx); err != nil {
		return fmt.Errorf("engine: ensure main conversation: %w", err)
	}
	// The free-time conversation must exist before ActiveConversations so it gets
	// a worker (no-op while free-time is disabled).
	if err := e.ensureFreeTimeConversation(ctx); err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	convs, err := e.store.ActiveConversations(ctx)
	if err != nil {
		return fmt.Errorf("engine: load active conversations: %w", err)
	}

	// Register the recurring maintenance tasks (each a no-op if already scheduled
	// or disabled). Kept here, not in New, so they run once startup is actually
	// committed to running the queue.
	e.ensureReflectionSchedule(ctx)
	e.ensureFreeTimeSchedule(ctx)
	e.ensureSubagentPruneSchedule(ctx)

	var wg sync.WaitGroup
	for _, c := range convs {
		kick := e.ensureWorker(c.ID)
		wg.Add(1)
		go func(id int64, kick chan struct{}) {
			defer wg.Done()
			e.worker(ctx, id, kick)
		}(c.ID, kick)
		e.kick(c.ID)
	}

	if ready != nil {
		close(ready)
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

// SubmitUserMessage appends a user message and kicks the conversation worker.
// It returns once the message is persisted; the run happens asynchronously.
//
// It first checks that the conversation has a processing lane: appending a
// message to a conversation with no worker would persist a row that never runs
// (the kick would silently no-op). Inbound transports start only after
// StartReady has registered and launched workers for active conversations.
func (e *Engine) SubmitUserMessage(ctx context.Context, conversationID int64, text, source string) error {
	e.mu.Lock()
	_, hasWorker := e.workers[conversationID]
	e.mu.Unlock()
	if !hasWorker {
		return fmt.Errorf("engine: no worker for conversation %d", conversationID)
	}
	_, err := e.store.AppendMessage(ctx, store.Message{
		ConversationID: conversationID,
		Role:           llm.RoleUser,
		Source:         source,
		Content:        store.Content{Text: text},
	})
	if err != nil {
		return fmt.Errorf("engine: submit user message: %w", err)
	}
	e.kick(conversationID)
	return nil
}

// Subscribe registers a callback for run events from all conversations.
// Delivery is synchronous; subscribers must not block. The returned func
// unsubscribes.
func (e *Engine) Subscribe(fn func(Event)) (cancel func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.nextSubID
	e.nextSubID++
	e.subscribers[id] = fn
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.subscribers, id)
	}
}

// AddNotifier registers a transport for out-of-band pushes.
func (e *Engine) AddNotifier(n Notifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifiers = append(e.notifiers, n)
}

func (e *Engine) emit(ev Event) {
	e.mu.Lock()
	subs := make([]func(Event), 0, len(e.subscribers))
	for _, fn := range e.subscribers {
		subs = append(subs, fn)
	}
	e.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (e *Engine) ensureWorker(conversationID int64) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if kick, ok := e.workers[conversationID]; ok {
		return kick
	}
	kick := make(chan struct{}, 1)
	e.workers[conversationID] = kick
	return kick
}

// kick wakes the conversation's worker without blocking; a pending kick is
// enough since the worker re-reads the transcript each iteration.
func (e *Engine) kick(conversationID int64) {
	e.mu.Lock()
	kick := e.workers[conversationID]
	e.mu.Unlock()
	if kick == nil {
		return
	}
	select {
	case kick <- struct{}{}:
	default:
	}
}

// worker drains due runs for one conversation. A run is due whenever a user
// message past the conversation's coverage cursor exists and no run is executing
// (D2). Each run answers the newest such message and advances the cursor, so a
// user message appended while a run is in flight is picked up on the next
// iteration rather than buried under the run's reply.
func (e *Engine) worker(ctx context.Context, conversationID int64, kick chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			input, ok, err := e.store.NextDueInput(ctx, conversationID)
			if err != nil {
				e.logf(errorLevel, "conversation %d: read due input: %v", conversationID, err)
				break
			}
			if !ok {
				break
			}
			e.executeRun(ctx, conversationID, input)
		}
	}
}
