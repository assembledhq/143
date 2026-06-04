package models

import (
	"encoding/json"
	"fmt"
	"time"
)

type NodeStatus string

const (
	NodeStatusActive   NodeStatus = "active"
	NodeStatusDraining NodeStatus = "draining"
	NodeStatusDead     NodeStatus = "dead"
)

func (s NodeStatus) Validate() error {
	switch s {
	case NodeStatusActive, NodeStatusDraining, NodeStatusDead:
		return nil
	default:
		return fmt.Errorf("invalid NodeStatus: %q", s)
	}
}

type NodeMode string

const (
	NodeModeAll    NodeMode = "all"
	NodeModeAPI    NodeMode = "api"
	NodeModeWorker NodeMode = "worker"
)

func (m NodeMode) Validate() error {
	switch m {
	case NodeModeAll, NodeModeAPI, NodeModeWorker:
		return nil
	default:
		return fmt.Errorf("invalid NodeMode: %q", m)
	}
}

// Node is a row in the cluster nodes table.
type Node struct {
	ID                   string          `db:"id" json:"id"`
	Mode                 NodeMode        `db:"mode" json:"mode"`
	Host                 string          `db:"host" json:"host"`
	Status               NodeStatus      `db:"status" json:"status"`
	DrainIntent          DrainIntent     `db:"drain_intent" json:"drain_intent"`
	Metadata             json.RawMessage `db:"metadata" json:"metadata"`
	StartedAt            time.Time       `db:"started_at" json:"started_at"`
	LastHeartbeatAt      time.Time       `db:"last_heartbeat_at" json:"last_heartbeat_at"`
	DrainRequestedAt     *time.Time      `db:"drain_requested_at" json:"drain_requested_at,omitempty"`
	DrainBudgetExpiresAt *time.Time      `db:"drain_budget_expires_at" json:"drain_budget_expires_at,omitempty"`
	DrainRequestedBy     string          `db:"drain_requested_by" json:"drain_requested_by"`
	DrainReason          string          `db:"drain_reason" json:"drain_reason"`
}
