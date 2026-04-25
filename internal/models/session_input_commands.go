package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// SessionInputCommands is the persisted collection of slash commands attached
// to a session message. Stored as JSON in the session_messages.commands
// column.
type SessionInputCommands []SessionInputCommand

func (c SessionInputCommands) Value() (driver.Value, error) {
	if len(c) == 0 {
		return []byte("[]"), nil
	}

	encoded, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal session input commands: %w", err)
	}
	return encoded, nil
}

func (c *SessionInputCommands) Scan(src any) error {
	if src == nil {
		*c = nil
		return nil
	}

	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported session input commands type %T", src)
	}

	if len(raw) == 0 {
		*c = nil
		return nil
	}

	var decoded []SessionInputCommand
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("unmarshal session input commands: %w", err)
	}
	*c = decoded
	return nil
}
