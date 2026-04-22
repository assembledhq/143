package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

type SessionInputReferenceKind string

const (
	SessionInputReferenceKindFile      SessionInputReferenceKind = "file"
	SessionInputReferenceKindDirectory SessionInputReferenceKind = "directory"
	SessionInputReferenceKindApp       SessionInputReferenceKind = "app"
	SessionInputReferenceKindPlugin    SessionInputReferenceKind = "plugin"
)

func (k SessionInputReferenceKind) Validate() error {
	switch k {
	case SessionInputReferenceKindFile,
		SessionInputReferenceKindDirectory,
		SessionInputReferenceKindApp,
		SessionInputReferenceKindPlugin:
		return nil
	default:
		return fmt.Errorf("invalid SessionInputReferenceKind: %q", k)
	}
}

type SessionInputReference struct {
	Kind    SessionInputReferenceKind `json:"kind"`
	Token   string                    `json:"token,omitempty"`
	Path    string                    `json:"path,omitempty"`
	ID      string                    `json:"id,omitempty"`
	Display string                    `json:"display"`
}

func (r SessionInputReference) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if r.Display == "" {
		return fmt.Errorf("display is required")
	}

	switch r.Kind {
	case SessionInputReferenceKindFile, SessionInputReferenceKindDirectory:
		if r.Path == "" {
			return fmt.Errorf("path is required for %s references", r.Kind)
		}
	case SessionInputReferenceKindApp, SessionInputReferenceKindPlugin:
		if r.ID == "" {
			return fmt.Errorf("id is required for %s references", r.Kind)
		}
	}

	return nil
}

type SessionInputReferences []SessionInputReference

func (r SessionInputReferences) Value() (driver.Value, error) {
	if len(r) == 0 {
		return []byte("[]"), nil
	}

	encoded, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal session input references: %w", err)
	}
	return encoded, nil
}

func (r *SessionInputReferences) Scan(src any) error {
	if src == nil {
		*r = nil
		return nil
	}

	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported session input references type %T", src)
	}

	if len(raw) == 0 {
		*r = nil
		return nil
	}

	var decoded []SessionInputReference
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("unmarshal session input references: %w", err)
	}
	*r = decoded
	return nil
}

type RepositoryTreeEntryType string

const (
	RepositoryTreeEntryTypeFile      RepositoryTreeEntryType = "blob"
	RepositoryTreeEntryTypeDirectory RepositoryTreeEntryType = "tree"
)

type RepositoryTreeEntry struct {
	Path string                  `json:"path"`
	Type RepositoryTreeEntryType `json:"type"`
}
