package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type describes the semantic role of a durable memory.
type Type string

const (
	TypeFact         Type = "fact"
	TypePreference   Type = "preference"
	TypeDecision     Type = "decision"
	TypeConstraint   Type = "constraint"
	TypeProcedure    Type = "procedure"
	TypeIncident     Type = "incident"
	TypeEpisode      Type = "episode"
	TypeProjectState Type = "project_state"
)

// Status describes whether a memory participates in default retrieval.
type Status string

const (
	StatusActive     Status = "active"
	StatusSuperseded Status = "superseded"
	StatusForgotten  Status = "forgotten"
	StatusExpired    Status = "expired"
	StatusDisputed   Status = "disputed"
)

// ScopeType identifies the boundary within which a memory may be retrieved.
type ScopeType string

const (
	ScopeTenant        ScopeType = "tenant"
	ScopePrincipal     ScopeType = "principal"
	ScopeProject       ScopeType = "project"
	ScopeKnowledgeBase ScopeType = "knowledge_base"
	ScopeAgent         ScopeType = "agent"
)

var ErrInvalidMemory = errors.New("invalid durable memory")

// Memory is a durable, provenance-bearing statement. PostgreSQL is authoritative.
type Memory struct {
	ID              string
	TenantID        string
	ScopeType       ScopeType
	ScopeID         string
	Type            Type
	Subject         string
	Content         string
	Importance      float64
	Confidence      float64
	Sensitivity     string
	SourceType      string
	SourceReference string
	ValidFrom       *time.Time
	ValidTo         *time.Time
	ExpiresAt       *time.Time
	SupersedesID    string
	Status          Status
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate checks durable memory invariants without consulting authorization policy.
func (m Memory) Validate() error {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.TenantID) == "" ||
		strings.TrimSpace(m.ScopeID) == "" || strings.TrimSpace(m.Subject) == "" ||
		strings.TrimSpace(m.Content) == "" || strings.TrimSpace(m.CreatedBy) == "" {
		return fmt.Errorf("%w: required fields must not be empty", ErrInvalidMemory)
	}
	if !knownType(m.Type) {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidMemory, m.Type)
	}
	if !knownScope(m.ScopeType) {
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidMemory, m.ScopeType)
	}
	if !knownStatus(m.Status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidMemory, m.Status)
	}
	if m.Importance < 0 || m.Importance > 1 || m.Confidence < 0 || m.Confidence > 1 {
		return fmt.Errorf("%w: importance and confidence must be between 0 and 1", ErrInvalidMemory)
	}
	if m.ValidFrom != nil && m.ValidTo != nil && m.ValidFrom.After(*m.ValidTo) {
		return fmt.Errorf("%w: valid_from must not be after valid_to", ErrInvalidMemory)
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() || m.UpdatedAt.Before(m.CreatedAt) {
		return fmt.Errorf("%w: timestamps are invalid", ErrInvalidMemory)
	}
	if strings.TrimSpace(m.SourceType) == "" || strings.TrimSpace(m.SourceReference) == "" {
		return fmt.Errorf("%w: provenance is required", ErrInvalidMemory)
	}
	return nil
}

// IsRetrievable reports whether the memory is active and currently valid.
func (m Memory) IsRetrievable(now time.Time) bool {
	if m.Status != StatusActive {
		return false
	}
	now = now.UTC()
	if m.ValidFrom != nil && now.Before(m.ValidFrom.UTC()) {
		return false
	}
	if m.ValidTo != nil && now.After(m.ValidTo.UTC()) {
		return false
	}
	if m.ExpiresAt != nil && !now.Before(m.ExpiresAt.UTC()) {
		return false
	}
	return true
}

// Supersede returns a new active memory and marks the receiver superseded.
func (m *Memory) Supersede(replacement Memory, at time.Time) (Memory, error) {
	if m == nil {
		return Memory{}, fmt.Errorf("%w: nil memory", ErrInvalidMemory)
	}
	if m.Status != StatusActive {
		return Memory{}, fmt.Errorf("%w: only an active memory can be superseded", ErrInvalidMemory)
	}
	if replacement.TenantID != m.TenantID || replacement.ScopeType != m.ScopeType || replacement.ScopeID != m.ScopeID {
		return Memory{}, fmt.Errorf("%w: replacement scope must match", ErrInvalidMemory)
	}
	replacement.SupersedesID = m.ID
	replacement.Status = StatusActive
	if replacement.CreatedAt.IsZero() {
		replacement.CreatedAt = at.UTC()
	}
	if replacement.UpdatedAt.IsZero() {
		replacement.UpdatedAt = at.UTC()
	}
	if err := replacement.Validate(); err != nil {
		return Memory{}, err
	}
	m.Status = StatusSuperseded
	m.UpdatedAt = at.UTC()
	return replacement, nil
}

func knownType(value Type) bool {
	switch value {
	case TypeFact, TypePreference, TypeDecision, TypeConstraint,
		TypeProcedure, TypeIncident, TypeEpisode, TypeProjectState:
		return true
	default:
		return false
	}
}

func knownScope(value ScopeType) bool {
	switch value {
	case ScopeTenant, ScopePrincipal, ScopeProject, ScopeKnowledgeBase, ScopeAgent:
		return true
	default:
		return false
	}
}

func knownStatus(value Status) bool {
	switch value {
	case StatusActive, StatusSuperseded, StatusForgotten, StatusExpired, StatusDisputed:
		return true
	default:
		return false
	}
}
