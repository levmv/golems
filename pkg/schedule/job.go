package schedule

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("schedule: not found")
	ErrInvalid  = errors.New("schedule: invalid")
)

// Trigger describes when a job should next run after a given time.
type Trigger interface {
	Next(after time.Time) (time.Time, bool)
}

type validatableTrigger interface {
	Validate() error
}

// JobSpec is the application-provided definition of scheduled work.
//
// The scheduler owns timing and coordination. The application owns payloads.
// Kind and Ref are routing fields that let the application load the real
// payload from its own storage.
type JobSpec struct {
	ID       string
	Kind     string
	Ref      string
	Group    string
	Trigger  Trigger
	Timezone string
	Timeout  time.Duration
	// InitialRun creates one bootstrap occurrence due on the first pass when no
	// run history exists. Future recurring runs advance from that initial DueAt.
	InitialRun bool
	Metadata   map[string]string
}

func (s JobSpec) Validate() error {
	if s.ID == "" {
		return errors.Join(ErrInvalid, errors.New("job ID is required"))
	}
	if s.Trigger == nil {
		return errors.Join(ErrInvalid, errors.New("job trigger is required"))
	}
	if trigger, ok := s.Trigger.(validatableTrigger); ok {
		if err := trigger.Validate(); err != nil {
			return errors.Join(ErrInvalid, err)
		}
	}
	if s.Timeout < 0 {
		return errors.Join(ErrInvalid, errors.New("job timeout cannot be negative"))
	}
	return nil
}

// Job is one due execution of a JobSpec.
type Job struct {
	Spec          JobSpec
	RunID         string
	DueAt         time.Time
	OccurrenceKey string
}

func (j Job) ID() string {
	return j.Spec.ID
}

// Runner executes application work for a due job.
type Runner interface {
	Run(ctx context.Context, job Job) error
}

type RunnerFunc func(ctx context.Context, job Job) error

func (f RunnerFunc) Run(ctx context.Context, job Job) error {
	return f(ctx, job)
}

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunSkipped   RunStatus = "skipped"
)

// RunRecord is scheduler-level execution history. It intentionally does not
// store business payloads.
type RunRecord struct {
	ID            string
	JobID         string
	OccurrenceKey string
	Kind          string
	Ref           string
	Group         string
	DueAt         time.Time
	StartedAt     time.Time
	FinishedAt    *time.Time
	Status        RunStatus
	Error         string
}
