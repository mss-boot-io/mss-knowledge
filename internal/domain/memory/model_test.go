package memory

import (
	"testing"
	"time"
)

func TestMemoryValidationAndRetrievalWindow(t *testing.T) {
	now := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)
	validFrom := now.Add(-time.Hour)
	validTo := now.Add(time.Hour)
	memory := Memory{
		ID:              "mem_1",
		TenantID:        "tenant_1",
		ScopeType:       ScopeProject,
		ScopeID:         "mss-knowledge",
		Type:            TypeDecision,
		Subject:         "Storage boundaries",
		Content:         "Redis is a rebuildable projection.",
		Importance:      0.9,
		Confidence:      1,
		Sensitivity:     "internal",
		SourceType:      "explicit_user_statement",
		SourceReference: "conversation:design",
		ValidFrom:       &validFrom,
		ValidTo:         &validTo,
		Status:          StatusActive,
		CreatedBy:       "principal_1",
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
	}
	if err := memory.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !memory.IsRetrievable(now) {
		t.Fatal("IsRetrievable(now) = false")
	}
	if memory.IsRetrievable(now.Add(2 * time.Hour)) {
		t.Fatal("IsRetrievable(after validity) = true")
	}
}

func TestMemorySupersedePreservesHistory(t *testing.T) {
	now := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)
	original := Memory{
		ID:              "mem_old",
		TenantID:        "tenant_1",
		ScopeType:       ScopeProject,
		ScopeID:         "mss-knowledge",
		Type:            TypeDecision,
		Subject:         "Architecture",
		Content:         "Use an in-memory-only source of truth.",
		Importance:      0.8,
		Confidence:      1,
		Sensitivity:     "internal",
		SourceType:      "decision",
		SourceReference: "adr:old",
		Status:          StatusActive,
		CreatedBy:       "principal_1",
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
	}
	replacement := Memory{
		ID:              "mem_new",
		TenantID:        "tenant_1",
		ScopeType:       ScopeProject,
		ScopeID:         "mss-knowledge",
		Type:            TypeDecision,
		Subject:         "Architecture",
		Content:         "Use S3 and PostgreSQL as durable truth.",
		Importance:      1,
		Confidence:      1,
		Sensitivity:     "internal",
		SourceType:      "decision",
		SourceReference: "adr:0001",
		CreatedBy:       "principal_1",
	}

	created, err := original.Supersede(replacement, now)
	if err != nil {
		t.Fatalf("Supersede() error = %v", err)
	}
	if original.Status != StatusSuperseded {
		t.Fatalf("original status = %q", original.Status)
	}
	if created.SupersedesID != original.ID || created.Status != StatusActive {
		t.Fatalf("unexpected replacement: %+v", created)
	}
}
