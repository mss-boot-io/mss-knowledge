package catalog

import "time"

// KnowledgeBase is the public, authorization-filtered catalog representation.
type KnowledgeBase struct {
	ID              string    `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Revision        int64     `json:"revision"`
	DefaultLanguage string    `json:"default_language,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}
