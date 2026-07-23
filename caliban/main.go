// Command caliban is a long-lived personal AI assistant: durable conversations
// reachable over Telegram and a web UI, with file-based memory, a sandboxed
// shell, reminders and scheduled turns, and silent self-maintenance (context
// compaction and persona reflection).
//
// main dispatches subcommands (see printUsage); serve is the daemon and stays
// wiring-only: load config, open the store, build the engine, start transports.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/charmbracelet/x/term"
	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/telegram"
	"github.com/levmv/golems/caliban/internal/tools"
	"github.com/levmv/golems/caliban/internal/web"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/hackernews"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/tasks"
	tasksqlite "github.com/levmv/golems/pkg/tasks/sqlite"
	"github.com/levmv/golems/pkg/webfetch"
	"github.com/levmv/golems/pkg/websearch"

	"golang.org/x/sync/errgroup"
)

const (
	defaultConfigPath     = "/etc/golems/caliban.json"
	defaultShellTimeout   = 120 * time.Second
	defaultShellMaxOutput = 32768
	defaultTelegramConvID = 1
	defaultWebConvID      = 2
	webReadHeaderTimeout  = 10 * time.Second
	webIdleTimeout        = 2 * time.Minute
	webMaxHeaderBytes     = 64 << 10
)

var taskStoreOptions = tasksqlite.Options{Table: "tasks"}

func main() {
	// The shell sandbox re-execs caliban into this entrypoint (the Landlock
	// trampoline): it sandboxes itself and becomes bash. Must run before any
	// other startup, and never returns on success.
	if len(os.Args) >= 2 && os.Args[1] == tools.SandboxArgv {
		tools.RunSandboxedShell()
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == tools.RunnerArgv {
		tools.RunSandboxedRunner()
		return
	}

	// Bootstrap logger for errors before any config (and its log level) is loaded.
	boot := logger.Default()

	switch err := dispatch(os.Args[1:]); {
	case err == nil:
		return
	case errors.Is(err, flag.ErrHelp):
		// The flag package already printed the command's usage; -h is not a failure.
		return
	case errors.Is(err, errUnknownCommand):
		os.Exit(2)
	default:
		boot.Error("caliban: %v", err)
		os.Exit(1)
	}
}

// errUnknownCommand signals an unrecognized subcommand; main maps it to exit code 2.
var errUnknownCommand = errors.New("unknown command")

// dispatch routes a subcommand to its handler. Each subcommand owns its flags
// (including -config), so flags follow the command: "caliban serve -config X".
// A bare invocation, "help", or any -h variant prints usage and exits cleanly.
func dispatch(args []string) error {
	cmd := ""
	var rest []string
	if len(args) > 0 {
		cmd = args[0]
		rest = args[1:]
	}
	switch cmd {
	case "", "help", "-h", "-help", "--help":
		printUsage(os.Stdout)
		return nil
	case "serve":
		return serveCommand(rest)
	case "check-config":
		return checkConfigCommand(rest)
	case "inspect-context", "debug-context":
		return inspectContext(rest)
	case "set-web-password":
		return setWebPasswordCommand(rest)
	case "generate-vapid-keys":
		return generateVAPIDKeys(rest)
	default:
		fmt.Fprintf(os.Stderr, "caliban: unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return errUnknownCommand
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Caliban — a long-lived personal AI assistant.

Usage:
  caliban <command> [flags]

Commands:
  serve                Run the assistant: Telegram/web transports and the task queue.
  check-config         Validate configuration without starting the assistant.
  inspect-context      Print a conversation's context state (summary, tail, compaction).
  set-web-password     Set or change the web UI password.
  generate-vapid-keys  Print a new Web Push VAPID key pair.
  help                 Show this help.

Run "caliban <command> -h" for command-specific flags.
`)
}

// serveCommand runs the long-lived assistant. This is the daemon entrypoint the
// systemd unit invokes; a bare "caliban" prints usage rather than starting it.
func serveCommand(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath, "path to config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return run(*configPath)
}

func checkConfigCommand(args []string) error {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath, "path to config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkConfig(*configPath); err != nil {
		return err
	}
	fmt.Printf("caliban: config OK (%s)\n", *configPath)
	return nil
}

// checkConfig exercises all static startup validation without opening state or
// making network requests. It catches model URIs, timezone/log settings, and
// optional tool wiring in addition to the strict JSON/schema checks.
func checkConfig(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	log, err := cfg.logger()
	if err != nil {
		return err
	}
	if _, err := cfg.timezone(); err != nil {
		return err
	}
	registry := cfg.registry()
	if _, err := cfg.model(registry, cfg.Models.Main, log); err != nil {
		return err
	}
	if cfg.Models.Cheap != "" {
		if _, err := cfg.model(registry, cfg.Models.Cheap, log); err != nil {
			return err
		}
	}
	if _, _, err := websearch.NewTool(cfg.webSearchCredentials()); err != nil {
		return fmt.Errorf("initialize web search: %w", err)
	}
	return nil
}

func setWebPasswordCommand(args []string) error {
	fs := flag.NewFlagSet("set-web-password", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath, "path to config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setWebPassword(*configPath)
}

func generateVAPIDKeys(args []string) error {
	fs := flag.NewFlagSet("generate-vapid-keys", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generate VAPID keys: %w", err)
	}
	fmt.Printf("{\n  \"vapid_public_key\": %q,\n  \"vapid_private_key\": %q,\n  \"subject\": \"admin@example.com\"\n}\n",
		publicKey, privateKey)
	return nil
}

func run(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	log, err := cfg.logger()
	if err != nil {
		return err
	}
	loc, err := cfg.timezone()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	webAuthEnabled := cfg.Web.Addr != "" && cfg.Web.Auth.enabled()
	if webAuthEnabled {
		_, ok, err := st.WebAuthPasswordHash(context.Background())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("web auth is enabled but no password is set; run: %s", setWebPasswordHint(configPath))
		}
	}

	ws, err := workspace.Open(cfg.WorkspacePath)
	if err != nil {
		return err
	}

	registry := cfg.registry()
	mainModel, err := cfg.model(registry, cfg.Models.Main, log)
	if err != nil {
		return err
	}
	// cheap powers compaction (M3); optional.
	var cheapModel golem.Model
	if cfg.Models.Cheap != "" {
		cheapModel, err = cfg.model(registry, cfg.Models.Cheap, log)
		if err != nil {
			return err
		}
	}

	// Tasks share the store's database. The handler mux routes by kind to the
	// engine; eng is bound below before the queue's RunLoop starts.
	if err := tasksqlite.EnsureSchema(context.Background(), st.DB(), taskStoreOptions); err != nil {
		return err
	}
	taskStore, err := tasksqlite.New(st.DB(), taskStoreOptions)
	if err != nil {
		return err
	}
	var eng *engine.Engine
	mux := tasks.HandlerFunc(func(ctx context.Context, t tasks.Task) error {
		switch t.Kind {
		case engine.KindReminderDeliver:
			return eng.HandleReminderDeliver(ctx, t)
		case engine.KindAgentTurn:
			return eng.HandleAgentTurn(ctx, t)
		case engine.KindCompaction:
			return eng.HandleCompaction(ctx, t)
		case engine.KindReflection:
			return eng.HandleReflection(ctx, t)
		case engine.KindFreeTime:
			return eng.HandleFreeTime(ctx, t)
		case engine.KindSubagentPrune:
			return eng.HandleSubagentPrune(ctx, t)
		default:
			return tasks.Discardf("unknown task kind %q", t.Kind)
		}
	})
	queue, err := tasks.New(taskStore, mux, tasks.Options{
		OnError:   func(err error) { log.Error("tasks: %v", err) },
		OnFailure: func(f tasks.Failure) { log.Warn("task %s failed: %v", f.Task.ID, f.Err) },
	})
	if err != nil {
		return err
	}

	shellTimeout := defaultShellTimeout
	if cfg.Shell.TimeoutSeconds > 0 {
		shellTimeout = time.Duration(cfg.Shell.TimeoutSeconds) * time.Second
	}
	shellMaxOutput := cfg.Shell.MaxOutputBytes
	if shellMaxOutput <= 0 {
		shellMaxOutput = defaultShellMaxOutput
	}
	shellSandbox := cfg.Shell.Sandbox
	if shellSandbox == "" {
		shellSandbox = tools.SandboxAuto
	}
	backgroundDir := filepath.Join(filepath.Dir(cfg.DBPath), "background-tasks")
	background, err := tools.NewBackgroundManager(st.DB(), ws.Root(), backgroundDir, shellMaxOutput, shellSandbox)
	if err != nil {
		return err
	}
	if err := background.ReconcileStartup(context.Background()); err != nil {
		return err
	}
	hnClient := hackernews.NewClient()
	builtinTools := []golem.Tool{
		tools.Shell(ws.Root(), shellTimeout, shellMaxOutput, shellSandbox, background),
		webfetch.NewTool(cfg.webFetchBackends(hnClient)...),
		hackernews.NewTool(hnClient),
	}
	searchTool, searchAvailable, err := websearch.NewTool(cfg.webSearchCredentials())
	if err != nil {
		return fmt.Errorf("initialize web search: %w", err)
	}
	if searchAvailable {
		builtinTools = append(builtinTools, searchTool)
	}
	builtinTools = append(builtinTools, tools.Memory(memoryToolStore{ws: ws})...)
	builtinTools = append(builtinTools, tools.BackgroundTaskTools(background)...)
	runners := tools.NewRunnerManager(ws.Root(), shellSandbox, shellMaxOutput, background)
	builtinTools = append(builtinTools, tools.RunnerTools(runners)...)
	skills, err := tools.NewBuiltinSkillLibrary()
	if err != nil {
		return err
	}
	builtinTools = append(builtinTools, tools.SkillTools(skills)...)
	skillCatalog := skills.FormatList()

	eng, err = engine.New(engine.Config{
		Store:             st,
		Workspace:         ws,
		Main:              mainModel,
		MainModelID:       cfg.Models.Main,
		Cheap:             cheapModel,
		CheapModelID:      cfg.Models.Cheap,
		Tools:             builtinTools,
		SkillCatalog:      skillCatalog,
		Tasks:             queue,
		TailBudgetTokens:  cfg.Context.TailBudgetTokens,
		KeepRecentTokens:  cfg.Context.KeepRecentTokens,
		MaxToolIterations: cfg.MaxToolIterations,
		Timezone:          loc,
		Logger:            log,
	})
	if err != nil {
		return err
	}

	var tg *telegram.Transport
	if cfg.Telegram.Token != "" {
		tgConvID := cfg.Telegram.ConversationID
		if tgConvID == 0 {
			tgConvID = defaultTelegramConvID
		}
		if _, err := st.EnsureConversation(context.Background(), tgConvID); err != nil {
			return err
		}
		tg, err = telegram.New(telegram.Config{
			Token:          cfg.Telegram.Token,
			ChatID:         cfg.Telegram.ChatID,
			ConversationID: tgConvID,
			Engine:         eng,
			Logger:         log,
		})
		if err != nil {
			return err
		}
		eng.AddNotifier(tg)
	}

	var webConvID int64
	if cfg.Web.Addr != "" {
		webConvID = cfg.Web.ConversationID
		if webConvID == 0 {
			webConvID = defaultWebConvID
		}
		if _, err := st.EnsureConversation(context.Background(), webConvID); err != nil {
			return err
		}
	}

	var srv *http.Server
	if cfg.Web.Addr != "" {
		webTransport := web.New(web.Config{
			Engine:         eng,
			Store:          st,
			ConversationID: webConvID,
			Auth:           web.AuthConfig{Enabled: webAuthEnabled},
			Push: web.PushConfig{
				VAPIDPublicKey:  cfg.Web.Push.VAPIDPublicKey,
				VAPIDPrivateKey: cfg.Web.Push.VAPIDPrivateKey,
				Subject:         cfg.Web.Push.Subject,
			},
			Logger: log,
		})
		eng.AddNotifier(webTransport)
		srv = &http.Server{
			Addr:              cfg.Web.Addr,
			Handler:           webTransport.Handler(),
			ReadHeaderTimeout: webReadHeaderTimeout,
			IdleTimeout:       webIdleTimeout,
			MaxHeaderBytes:    webMaxHeaderBytes,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("caliban: starting (db=%s workspace=%s)", cfg.DBPath, ws.Root())
	g, gctx := errgroup.WithContext(ctx)
	engineReady := make(chan struct{})
	g.Go(func() error { return eng.StartReady(gctx, engineReady) })
	g.Go(func() error {
		<-gctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		return background.StopAll(sctx, "caliban shutdown")
	})

	select {
	case <-engineReady:
	case <-gctx.Done():
		if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	if tg != nil {
		g.Go(func() error { return tg.Run(gctx) })
	}
	g.Go(func() error { return queue.RunLoop(gctx) })

	if srv != nil {
		g.Go(func() error {
			log.Info("caliban: web transport on %s", cfg.Web.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-gctx.Done()
			sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			return srv.Shutdown(sctx)
		})
	}

	// A signal-cancelled context is a clean shutdown, not a failure.
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("caliban: stopped")
	return nil
}

func setWebPassword(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	password, err := promptPassword()
	if err != nil {
		return err
	}
	hash, err := web.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.SetWebAuthPasswordHash(context.Background(), hash); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Web password updated.")
	return nil
}

func setWebPasswordHint(configPath string) string {
	if configPath == "" || configPath == defaultConfigPath {
		return "caliban set-web-password"
	}
	return fmt.Sprintf("caliban set-web-password -config %s", configPath)
}

func promptPassword() (string, error) {
	fmt.Fprint(os.Stderr, "New web password: ")
	first, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	fmt.Fprint(os.Stderr, "Repeat web password: ")
	second, err := term.ReadPassword(os.Stdin.Fd())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password confirmation: %w", err)
	}
	password := string(first)
	if password != string(second) {
		return "", fmt.Errorf("passwords do not match")
	}
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("password is required")
	}
	return password, nil
}
