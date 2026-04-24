package models

import (
	"encoding/json"
	"time"
)

// Node is a row in the cluster nodes table.
type Node struct {
	ID              string          `db:"id" json:"id"`
	Mode            string          `db:"mode" json:"mode"`
	Host            string          `db:"host" json:"host"`
	Status          string          `db:"status" json:"status"`
	Metadata        json.RawMessage `db:"metadata" json:"metadata"`
	StartedAt       time.Time       `db:"started_at" json:"started_at"`
	LastHeartbeatAt time.Time       `db:"last_heartbeat_at" json:"last_heartbeat_at"`
}
