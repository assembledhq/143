package config

import (
	"net"
	"strings"
)

// SandboxHostID returns only explicitly provisioned, canonical machine
// identity. Missing or invalid configuration deliberately supplies no budget.
func (c Config) SandboxHostID() string {
	if ip := net.ParseIP(strings.TrimSpace(c.WorkerPrivateIP)); ip != nil && ip.IsGlobalUnicast() {
		return ip.String()
	}
	return ""
}
