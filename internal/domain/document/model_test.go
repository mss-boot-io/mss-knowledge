package document

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewVersionAndLifecycle(t *testing.T) {
	createdAt := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	version, err := NewVersion(
		"ver_1",
		"doc_1",
		"kb_1",
		1,
		"architecture.md",
		strings.Repeat("a", 64),
		"pipeline-v1",
		createdAt,
	)
	if err != nil {
		t.Fatalf("NewVersion() error = %v", err)
	}
	if version.Status != VersionStatusProcessing {
		t.Fatalf("Status = %q, want %q", version.Status, VersionStatusProcessing)
	}

	publishedAt := createdAt.Add(time.Minute)
	if err := version.Transition(VersionStatusReady, publishedAt); err != nil {
		t.Fatalf("Transition(READY) error = %v", err)
	}
	if version.PublishedAt == nil || !version.PublishedAt.Equal(publishedAt) {
		t.Fatalf("PublishedAt = %v, want %v", version.PublishedAt, publishedAt)
	}

	supersededAt := publishedAt.Add(time.Minute)
	if err := version.Transition(VersionStatusSuperseded, supersededAt); err != nil {
		t.Fatalf("Transition(SUPERSEDED) error = %v", err)
	}
	if version.SupersededAt == nil || !version.SupersededAt.Equal(supersededAt) {
		t.Fatalf("SupersededAt = %v, want %v", version.SupersededAt, supersededAt)
	}
}

func TestVersionRejectsInvalidHash(t *testing.T) {
	_, err := NewVersion(
		"ver_1",
		"doc_1",
		"kb_1",
		1,
		"architecture.md",
		"not-a-hash",
		"pipeline-v1",
		time.Now(),
	)
	if !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("NewVersion() error = %v, want ErrInvalidVersion", err)
	}
}

func TestVersionRejectsMixedPublicationTransition(t *testing.T) {
	version, err := NewVersion(
		"ver_1",
		"doc_1",
		"kb_1",
		1,
		"architecture.md",
		strings.Repeat("b", 64),
		"pipeline-v1",
		time.Now().Add(-time.Minute),
	)
	if err != nil {
		t.Fatalf("NewVersion() error = %v", err)
	}

	if err := version.Transition(VersionStatusSuperseded, time.Now()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition() error = %v, want ErrInvalidTransition", err)
	}
}

func TestCanTransitionDeletedIsTerminal(t *testing.T) {
	statuses := []VersionStatus{
		VersionStatusProcessing,
		VersionStatusReady,
		VersionStatusFailed,
		VersionStatusQuarantined,
		VersionStatusSuperseded,
		VersionStatusDeleted,
	}
	for _, status := range statuses {
		if CanTransition(VersionStatusDeleted, status) {
			t.Fatalf("deleted version unexpectedly transitions to %q", status)
		}
	}
}
