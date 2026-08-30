package ingestion

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// JobID identifies a durable ingestion job.
type JobID string

// State describes worker scheduling and terminal outcome.
type State string

const (
	StatePending   State = "PENDING"
	StateRunning   State = "RUNNING"
	StateRetryWait State = "RETRY_WAIT"
	StateSucceeded State = "SUCCEEDED"
	StateFailed    State = "FAILED"
	StateCancelled State = "CANCELLED"
)

// Stage describes the deterministic document-processing sequence.
type Stage string

const (
	StageReceived    Stage = "RECEIVED"
	StageStored      Stage = "STORED"
	StageValidating  Stage = "VALIDATING"
	StageParsing     Stage = "PARSING"
	StageNormalizing Stage = "NORMALIZING"
	StageChunking    Stage = "CHUNKING"
	StageEmbedding   Stage = "EMBEDDING"
	StageIndexing    Stage = "INDEXING"
	StageVerifying   Stage = "VERIFYING"
	StagePublishing  Stage = "PUBLISHING"
	StageReady       Stage = "READY"
)

var (
	// ErrInvalidJob is returned when a job violates domain invariants.
	ErrInvalidJob = errors.New("invalid ingestion job")
	// ErrInvalidStateTransition is returned for forbidden scheduling transitions.
	ErrInvalidStateTransition = errors.New("invalid ingestion state transition")
	// ErrInvalidStageTransition is returned when a stage is skipped or reversed.
	ErrInvalidStageTransition = errors.New("invalid ingestion stage transition")
	// ErrLeaseMismatch is returned when a worker does not own the active lease.
	ErrLeaseMismatch = errors.New("ingestion lease mismatch")
)

var stageOrder = []Stage{
	StageReceived,
	StageStored,
	StageValidating,
	StageParsing,
	StageNormalizing,
	StageChunking,
	StageEmbedding,
	StageIndexing,
	StageVerifying,
	StagePublishing,
	StageReady,
}

// Job models the durable scheduling and progress state for one processing job.
type Job struct {
	ID              JobID
	TenantID        string
	KnowledgeBaseID string
	DocumentID      string
	VersionID       string
	Kind            string
	State           State
	Stage           Stage
	Attempt         int
	MaxAttempts     int
	LeaseOwner      string
	LeaseExpiresAt  *time.Time
	NextAttemptAt   time.Time
	ErrorCode       string
	ErrorMessage    string
	CreatedAt       time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	UpdatedAt       time.Time
}

// NewJob creates a pending job at the RECEIVED stage.
func NewJob(
	id JobID,
	tenantID string,
	knowledgeBaseID string,
	documentID string,
	versionID string,
	kind string,
	maxAttempts int,
	createdAt time.Time,
) (Job, error) {
	job := Job{
		ID:              id,
		TenantID:        strings.TrimSpace(tenantID),
		KnowledgeBaseID: strings.TrimSpace(knowledgeBaseID),
		DocumentID:      strings.TrimSpace(documentID),
		VersionID:       strings.TrimSpace(versionID),
		Kind:            strings.TrimSpace(kind),
		State:           StatePending,
		Stage:           StageReceived,
		MaxAttempts:     maxAttempts,
		NextAttemptAt:   createdAt.UTC(),
		CreatedAt:       createdAt.UTC(),
		UpdatedAt:       createdAt.UTC(),
	}
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Validate checks durable job invariants.
func (j Job) Validate() error {
	if strings.TrimSpace(string(j.ID)) == "" ||
		j.TenantID == "" ||
		j.KnowledgeBaseID == "" ||
		j.DocumentID == "" ||
		j.VersionID == "" ||
		j.Kind == "" {
		return fmt.Errorf("%w: identity fields must not be empty", ErrInvalidJob)
	}
	if j.MaxAttempts <= 0 {
		return fmt.Errorf("%w: max attempts must be positive", ErrInvalidJob)
	}
	if j.Attempt < 0 || j.Attempt > j.MaxAttempts {
		return fmt.Errorf("%w: attempt is out of range", ErrInvalidJob)
	}
	if stageIndex(j.Stage) < 0 {
		return fmt.Errorf("%w: unknown stage %q", ErrInvalidJob, j.Stage)
	}
	if !knownState(j.State) {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidJob, j.State)
	}
	if j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() || j.NextAttemptAt.IsZero() {
		return fmt.Errorf("%w: timestamps must not be zero", ErrInvalidJob)
	}
	if j.State == StateRunning {
		if j.LeaseOwner == "" || j.LeaseExpiresAt == nil {
			return fmt.Errorf("%w: running job requires a lease", ErrInvalidJob)
		}
	}
	return nil
}

// Claim moves an eligible job to RUNNING and assigns a bounded lease.
func (j *Job) Claim(owner string, now, leaseExpiresAt time.Time) error {
	if j == nil {
		return fmt.Errorf("%w: nil job", ErrInvalidJob)
	}
	owner = strings.TrimSpace(owner)
	now = now.UTC()
	leaseExpiresAt = leaseExpiresAt.UTC()
	if owner == "" || now.IsZero() || !leaseExpiresAt.After(now) {
		return fmt.Errorf("%w: invalid lease", ErrInvalidJob)
	}
	if j.State != StatePending && j.State != StateRetryWait {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStateTransition, j.State, StateRunning)
	}
	if now.Before(j.NextAttemptAt) {
		return fmt.Errorf("%w: next attempt is not due", ErrInvalidStateTransition)
	}
	if j.Attempt >= j.MaxAttempts {
		return fmt.Errorf("%w: attempts exhausted", ErrInvalidStateTransition)
	}

	j.State = StateRunning
	j.Attempt++
	j.LeaseOwner = owner
	j.LeaseExpiresAt = &leaseExpiresAt
	j.UpdatedAt = now
	if j.StartedAt == nil {
		j.StartedAt = &now
	}
	j.ErrorCode = ""
	j.ErrorMessage = ""
	return nil
}

// Advance moves a running job to the immediately following pipeline stage.
func (j *Job) Advance(owner string, to Stage, now time.Time) error {
	if err := j.requireLease(owner, now); err != nil {
		return err
	}
	if !CanAdvance(j.Stage, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStageTransition, j.Stage, to)
	}
	j.Stage = to
	j.UpdatedAt = now.UTC()
	return nil
}

// Retry schedules a retry after a retryable failure and releases the lease.
func (j *Job) Retry(owner string, nextAttemptAt time.Time, code, message string, now time.Time) error {
	if err := j.requireLease(owner, now); err != nil {
		return err
	}
	now = now.UTC()
	nextAttemptAt = nextAttemptAt.UTC()
	if !nextAttemptAt.After(now) {
		return fmt.Errorf("%w: retry time must be in the future", ErrInvalidJob)
	}
	if j.Attempt >= j.MaxAttempts {
		return j.Fail(owner, code, message, now)
	}
	j.State = StateRetryWait
	j.NextAttemptAt = nextAttemptAt
	j.ErrorCode = strings.TrimSpace(code)
	j.ErrorMessage = strings.TrimSpace(message)
	j.clearLease()
	j.UpdatedAt = now
	return nil
}

// Fail records a terminal processing failure and releases the lease.
func (j *Job) Fail(owner, code, message string, now time.Time) error {
	if err := j.requireLease(owner, now); err != nil {
		return err
	}
	now = now.UTC()
	j.State = StateFailed
	j.ErrorCode = strings.TrimSpace(code)
	j.ErrorMessage = strings.TrimSpace(message)
	j.CompletedAt = &now
	j.UpdatedAt = now
	j.clearLease()
	return nil
}

// Complete marks a READY-stage job as successful.
func (j *Job) Complete(owner string, now time.Time) error {
	if err := j.requireLease(owner, now); err != nil {
		return err
	}
	if j.Stage != StageReady {
		return fmt.Errorf("%w: cannot complete at stage %s", ErrInvalidStateTransition, j.Stage)
	}
	now = now.UTC()
	j.State = StateSucceeded
	j.CompletedAt = &now
	j.UpdatedAt = now
	j.clearLease()
	return nil
}

// CanAdvance reports whether to is the immediately following stage.
func CanAdvance(from, to Stage) bool {
	fromIndex := stageIndex(from)
	toIndex := stageIndex(to)
	return fromIndex >= 0 && toIndex == fromIndex+1
}

// NextStage returns the next pipeline stage.
func NextStage(stage Stage) (Stage, bool) {
	index := stageIndex(stage)
	if index < 0 || index+1 >= len(stageOrder) {
		return "", false
	}
	return stageOrder[index+1], true
}

func (j *Job) requireLease(owner string, now time.Time) error {
	if j == nil {
		return fmt.Errorf("%w: nil job", ErrInvalidJob)
	}
	if j.State != StateRunning {
		return fmt.Errorf("%w: job is %s", ErrInvalidStateTransition, j.State)
	}
	if strings.TrimSpace(owner) == "" || owner != j.LeaseOwner {
		return ErrLeaseMismatch
	}
	if j.LeaseExpiresAt == nil || !now.UTC().Before(*j.LeaseExpiresAt) {
		return fmt.Errorf("%w: lease expired", ErrLeaseMismatch)
	}
	return nil
}

func (j *Job) clearLease() {
	j.LeaseOwner = ""
	j.LeaseExpiresAt = nil
}

func stageIndex(stage Stage) int {
	for i, candidate := range stageOrder {
		if candidate == stage {
			return i
		}
	}
	return -1
}

func knownState(state State) bool {
	switch state {
	case StatePending, StateRunning, StateRetryWait, StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}
