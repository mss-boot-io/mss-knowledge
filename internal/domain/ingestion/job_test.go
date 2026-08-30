package ingestion

import (
	"errors"
	"testing"
	"time"
)

func TestJobRequiresOrderedStagesAndLeaseOwner(t *testing.T) {
	now := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	job, err := NewJob("job_1", "tenant_1", "kb_1", "doc_1", "ver_1", "ingest", 3, now)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}

	if err := job.Claim("worker-a", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if job.Attempt != 1 || job.State != StateRunning {
		t.Fatalf("unexpected claimed job: %+v", job)
	}

	if err := job.Advance("worker-b", StageStored, now.Add(time.Second)); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("Advance() with wrong owner error = %v, want ErrLeaseMismatch", err)
	}
	if err := job.Advance("worker-a", StageParsing, now.Add(time.Second)); !errors.Is(err, ErrInvalidStageTransition) {
		t.Fatalf("Advance() skipping stage error = %v, want ErrInvalidStageTransition", err)
	}
	if err := job.Advance("worker-a", StageStored, now.Add(time.Second)); err != nil {
		t.Fatalf("Advance(STORED) error = %v", err)
	}
}

func TestJobRetryAndComplete(t *testing.T) {
	now := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	job, err := NewJob("job_1", "tenant_1", "kb_1", "doc_1", "ver_1", "ingest", 3, now)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}

	if err := job.Claim("worker-a", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := job.Retry("worker-a", now.Add(10*time.Second), "DEPENDENCY_UNAVAILABLE", "temporary", now.Add(time.Second)); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if job.State != StateRetryWait || job.LeaseOwner != "" || job.LeaseExpiresAt != nil {
		t.Fatalf("unexpected retry state: %+v", job)
	}

	claimAt := now.Add(10 * time.Second)
	if err := job.Claim("worker-b", claimAt, claimAt.Add(time.Minute)); err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}

	for {
		next, ok := NextStage(job.Stage)
		if !ok {
			break
		}
		claimAt = claimAt.Add(time.Second)
		if err := job.Advance("worker-b", next, claimAt); err != nil {
			t.Fatalf("Advance(%s) error = %v", next, err)
		}
	}
	if job.Stage != StageReady {
		t.Fatalf("Stage = %q, want READY", job.Stage)
	}
	if err := job.Complete("worker-b", claimAt.Add(time.Second)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if job.State != StateSucceeded || job.CompletedAt == nil {
		t.Fatalf("unexpected completed job: %+v", job)
	}
}

func TestJobExhaustedRetryFails(t *testing.T) {
	now := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	job, err := NewJob("job_1", "tenant_1", "kb_1", "doc_1", "ver_1", "ingest", 1, now)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if err := job.Claim("worker-a", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := job.Retry("worker-a", now.Add(time.Minute), "TIMEOUT", "failed", now.Add(time.Second)); err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if job.State != StateFailed {
		t.Fatalf("State = %q, want FAILED", job.State)
	}
}

func TestExpiredLeaseCannotAdvance(t *testing.T) {
	now := time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC)
	job, err := NewJob("job_1", "tenant_1", "kb_1", "doc_1", "ver_1", "ingest", 3, now)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if err := job.Claim("worker-a", now, now.Add(time.Second)); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := job.Advance("worker-a", StageStored, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("Advance() error = %v, want ErrLeaseMismatch", err)
	}
}
