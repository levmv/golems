package engine

import (
	"context"
	"fmt"

	"github.com/levmv/golems/pkg/llm"
)

// runMaintenance executes one silent maintenance pass under a run record. It is
// the common ground between compaction and self-reflection: both open a run
// (initiator "agent", no input message), do a single model call plus an artifact
// write, and record the run's terminal status — but append nothing to the
// transcript and emit no stream events, so the user never sees them.
//
// fn does the model call and persists whatever artifact (a summary row, the
// persona file), returning the usage to attribute to the run. The run is marked
// done when fn returns nil and failed otherwise; fn's error is returned to the
// caller unchanged so it can decide whether the task queue should retry.
func (e *Engine) runMaintenance(ctx context.Context, conversationID int64, modelID string, fn func(context.Context) (llm.Usage, error)) error {
	run, err := e.store.CreateRun(ctx, conversationID, "agent", modelID, 0)
	if err != nil {
		return fmt.Errorf("create maintenance run: %w", err)
	}
	usage, err := fn(ctx)
	status, errMsg := "done", ""
	if err != nil {
		status, errMsg = "failed", err.Error()
	}
	if ferr := e.store.FinishRun(ctx, run.ID, status, usage, errMsg); ferr != nil {
		e.logf(warnLevel, "maintenance run %d: finish: %v", run.ID, ferr)
	}
	return err
}
