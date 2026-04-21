package adapters

import "github.com/assembledhq/143/internal/services/agent"

// drain reads every entry from a closed LogEntry channel. Shared by the
// stream-JSON tests across adapters (amp, pi, stream_parser) — each of which
// drives parseAgentStreamLine or an adapter's Execute and needs to snapshot
// the emitted log events.
func drain(ch chan agent.LogEntry) []agent.LogEntry {
	var out []agent.LogEntry
	for entry := range ch {
		out = append(out, entry)
	}
	return out
}
