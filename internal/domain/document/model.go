package document

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DocumentID identifies a logical document across immutable versions.
type DocumentID string

// VersionID identifies one immutable processing and publication attempt.
type VersionID string

// KnowledgeBaseID identifies the owning knowledge base.
type KnowledgeBaseID string

// VersionStatus describes the publication lifecycle of a document version.
type VersionStatus string

const (
	VersionStatusProcessing  VersionStatus = "PROCESSING"
	VersionStatusReady       VersionStatus = "READY"
	VersionStatusFailed      VersionStatus = "FAILED"
	VersionStatusQuarantined VersionStatus = "QUARANTINED"
	VersionStatusSuperseded  VersionStatus = "SUPERSEDED"
	VersionStatusDeleted     VersionStatus = "DELETED"
)

var (
	// ErrInvalidTransition is returned when a version lifecycle transition is forbidden.
	ErrInvalidTransition = errors.New("invalid document version transition")
	// ErrInvalidVersion is returned when version invariants are not satisfied.
	ErrInvalidVersion = errors.New("invalid document version")
)

// Version is the durable domain representation of one immutable document version.
type Version struct {
	ID                  VersionID
	DocumentID          DocumentID
	KnowledgeBaseID     KnowledgeBaseID
	Number              int64
	Status              VersionStatus
	Filename            string
	MediaType           string
	ContentSHA256       string
	PipelineFingerprint string
	CreatedAt           time.Time
	PublishedAt         *time.Time
	SupersededAt        *time.Time
	DeletedAt           *time.Time
}

// NewVersion creates a processing version after validating immutable identity fields.
func NewVersion(
	id VersionID,
	documentID DocumentID,
	knowledgeBaseID KnowledgeBaseID,
	number int64,
	filename string,
	contentSHA256 string,
	pipelineFingerprint string,
	createdAt time.Time,
) (Version, error) {
	version := Version{
		ID:                  id,
		DocumentID:          documentID,
		KnowledgeBaseID:     knowledgeBaseID,
		Number:              number,
		Status:              VersionStatusProcessing,
		Filename:            strings.TrimSpace(filename),
		ContentSHA256:       strings.ToLower(strings.TrimSpace(contentSHA256)),
		PipelineFingerprint: strings.TrimSpace(pipelineFingerprint),
		CreatedAt:           createdAt.UTC(),
	}
	if err := version.Validate(); err != nil {
		return Version{}, err
	}
	return version, nil
}

// Validate checks identity and lifecycle invariants independent of persistence.
func (v Version) Validate() error {
	if strings.TrimSpace(string(v.ID)) == "" ||
		strings.TrimSpace(string(v.DocumentID)) == "" ||
		strings.TrimSpace(string(v.KnowledgeBaseID)) == "" {
		return fmt.Errorf("%w: IDs must not be empty", ErrInvalidVersion)
	}
	if v.Number <= 0 {
		return fmt.Errorf("%w: version number must be positive", ErrInvalidVersion)
	}
	if v.Filename == "" {
		return fmt.Errorf("%w: filename must not be empty", ErrInvalidVersion)
	}
	if len(v.ContentSHA256) != 64 || !isLowerHex(v.ContentSHA256) {
		return fmt.Errorf("%w: content SHA-256 must be 64 lowercase hexadecimal characters", ErrInvalidVersion)
	}
	if v.PipelineFingerprint == "" {
		return fmt.Errorf("%w: pipeline fingerprint must not be empty", ErrInvalidVersion)
	}
	if v.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created time must not be zero", ErrInvalidVersion)
	}
	if !knownStatus(v.Status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidVersion, v.Status)
	}
	return nil
}

// Transition applies a permitted lifecycle transition at the supplied time.
func (v *Version) Transition(to VersionStatus, at time.Time) error {
	if v == nil {
		return fmt.Errorf("%w: nil version", ErrInvalidVersion)
	}
	if !CanTransition(v.Status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, v.Status, to)
	}
	at = at.UTC()
	if at.IsZero() || at.Before(v.CreatedAt) {
		return fmt.Errorf("%w: transition time is invalid", ErrInvalidVersion)
	}

	v.Status = to
	switch to {
	case VersionStatusReady:
		v.PublishedAt = &at
	case VersionStatusSuperseded:
		v.SupersededAt = &at
	case VersionStatusDeleted:
		v.DeletedAt = &at
	}
	return nil
}

// CanTransition reports whether the version lifecycle permits the transition.
func CanTransition(from, to VersionStatus) bool {
	if from == to {
		return false
	}
	switch from {
	case VersionStatusProcessing:
		return to == VersionStatusReady ||
			to == VersionStatusFailed ||
			to == VersionStatusQuarantined ||
			to == VersionStatusDeleted
	case VersionStatusQuarantined:
		return to == VersionStatusProcessing || to == VersionStatusDeleted
	case VersionStatusReady:
		return to == VersionStatusSuperseded || to == VersionStatusDeleted
	case VersionStatusFailed:
		return to == VersionStatusDeleted
	case VersionStatusSuperseded:
		return to == VersionStatusReady || to == VersionStatusDeleted
	case VersionStatusDeleted:
		return false
	default:
		return false
	}
}

func knownStatus(status VersionStatus) bool {
	switch status {
	case VersionStatusProcessing,
		VersionStatusReady,
		VersionStatusFailed,
		VersionStatusQuarantined,
		VersionStatusSuperseded,
		VersionStatusDeleted:
		return true
	default:
		return false
	}
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
