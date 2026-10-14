package models

import (
	"fmt"
	"regexp"
	"strings"
)

var fullDockerContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// SandboxContainerReference binds a physical presence observation to the exact
// durable container and worker owner. Its host comes from the worker heartbeat.
type SandboxContainerReference struct {
	ContainerID string `db:"container_id" json:"container_id"`
	OwnerNodeID string `db:"owner_node_id" json:"owner_node_id"`
}

// IsFullDockerContainerID excludes reusable names and ambiguous short prefixes.
func IsFullDockerContainerID(id string) bool {
	return fullDockerContainerID.MatchString(id)
}

func (r SandboxContainerReference) Validate() error {
	if !IsFullDockerContainerID(r.ContainerID) {
		return fmt.Errorf("sandbox presence requires a complete Docker container ID")
	}
	if r.OwnerNodeID == "" || strings.TrimSpace(r.OwnerNodeID) != r.OwnerNodeID {
		return fmt.Errorf("sandbox presence requires an exact worker owner")
	}
	return nil
}
