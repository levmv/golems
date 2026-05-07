package schedule

import (
	"context"
	"sync"
)

type Middleware func(Runner) Runner

func Chain(runner Runner, middleware ...Middleware) Runner {
	for i := len(middleware) - 1; i >= 0; i-- {
		runner = middleware[i](runner)
	}
	return runner
}

// GroupConcurrency limits concurrent runner calls per JobSpec.Group.
func GroupConcurrency(limit int) Middleware {
	if limit <= 0 {
		limit = 1
	}

	groups := newGroupLimiter(limit)
	return func(next Runner) Runner {
		return RunnerFunc(func(ctx context.Context, job Job) error {
			release, err := groups.Acquire(ctx, job.Spec.Group)
			if err != nil {
				return err
			}
			defer release()

			return next.Run(ctx, job)
		})
	}
}

type groupLimiter struct {
	mu     sync.Mutex
	limit  int
	groups map[string]*groupState
}

type groupState struct {
	ch      chan struct{}
	waiters int
}

func newGroupLimiter(limit int) *groupLimiter {
	return &groupLimiter{
		limit:  limit,
		groups: make(map[string]*groupState),
	}
}

func (l *groupLimiter) Acquire(ctx context.Context, group string) (func(), error) {
	if group == "" {
		return func() {}, nil
	}

	l.mu.Lock()
	state, ok := l.groups[group]
	if !ok {
		state = &groupState{ch: make(chan struct{}, l.limit)}
		l.groups[group] = state
	}
	state.waiters++
	l.mu.Unlock()

	var acquired bool
	defer func() {
		l.mu.Lock()
		state.waiters--
		l.cleanupGroupLocked(group, state)
		l.mu.Unlock()
	}()

	select {
	case state.ch <- struct{}{}:
		acquired = true
		return func() {
			<-state.ch
			l.mu.Lock()
			l.cleanupGroupLocked(group, state)
			l.mu.Unlock()
		}, nil
	case <-ctx.Done():
		if acquired {
			<-state.ch
		}
		return nil, ctx.Err()
	}
}

func (l *groupLimiter) cleanupGroupLocked(group string, state *groupState) {
	current, ok := l.groups[group]
	if !ok || current != state {
		return
	}
	if len(state.ch) == 0 && state.waiters == 0 {
		delete(l.groups, group)
	}
}
