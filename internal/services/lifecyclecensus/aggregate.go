package lifecyclecensus

import (
	"encoding/hex"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func assemble(writers db.LifecycleCensusWriterSnapshot, connections db.LifecycleCensusConnectionSnapshot) Report {
	report := Report{
		SchemaVersion: 1,
		Limitations: []string{
			"passive partial observations; not a compatibility, liveness, drain or rollout verdict",
			"writer leases and activity have separate statement clocks; not an atomic snapshot or instance-to-backend join",
			"role/build labels are self-reported and spoofable; transaction-pooling proxies may not preserve them",
			"current database only; collector excluded; masked backend types are retained as unknown",
			"null, disabled or hidden activity/transaction details remain unknown, not evidence of no transaction",
			"at most 1000 rows per source; truncated counts describe only the retained sample, not source totals",
		},
		Writers:     WriterSummary{ObservedAt: writers.ObservedAt.UTC(), Truncated: writers.Truncated, CountsScope: countsScope(writers.Truncated), RowsObserved: len(writers.Rows), Groups: []WriterGroup{}},
		Connections: ConnectionSummary{ObservedAt: connections.ObservedAt.UTC(), Visibility: VisibilityUnknown, Truncated: connections.Truncated, CountsScope: countsScope(connections.Truncated), RowsObserved: len(connections.Rows), Groups: []ConnectionGroup{}},
	}
	if connections.FullVisibility != nil {
		if *connections.FullVisibility {
			report.Connections.Visibility = VisibilityFull
		} else {
			report.Connections.Visibility = VisibilityRestricted
		}
	}
	type writerKey struct {
		role    ObservedRole
		build   string
		version int
	}
	writerGroups := make(map[writerKey]int)
	for _, writer := range writers.Rows {
		role := safeRole(writer.Role)
		// Reuse the runtime formatter's exact full-SHA/dev/fingerprint policy.
		label := db.LifecycleConnectionApplicationName(models.LifecycleWriterRoleAPI, writer.BuildSHA)
		build := strings.TrimPrefix(label, "143:api:")
		writerGroups[writerKey{role, build, writer.Version}]++
	}
	for key, count := range writerGroups {
		report.Writers.Groups = append(report.Writers.Groups, WriterGroup{Role: key.role, Build: key.build, Version: key.version, Count: count})
	}
	sort.Slice(report.Writers.Groups, func(i, j int) bool {
		a, b := report.Writers.Groups[i], report.Writers.Groups[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		if a.Build != b.Build {
			return a.Build < b.Build
		}
		return a.Version < b.Version
	})
	type connectionKey struct {
		role  ObservedRole
		build string
		state ActivityState
	}
	connectionGroups := make(map[connectionKey]ConnectionGroup)
	for _, connection := range connections.Rows {
		role, build := parseLabel(connection.ApplicationName)
		state := safeState(connection.State, connection.DetailsVisible)
		key := connectionKey{role, build, state}
		group := connectionGroups[key]
		group.Role, group.Build, group.State = role, build, state
		group.Count++
		if connection.BackendType == nil {
			group.UnknownBackendType++
		}
		if !connection.DetailsVisible || state == StateUnknown || state == StateDisabled || connection.XactStart == nil {
			group.UnknownTransactionDetails++
		} else {
			group.ObservedOpenTransactions++
			if group.OldestObservedTransactionAt == nil || connection.XactStart.Before(*group.OldestObservedTransactionAt) {
				stamp := connection.XactStart.UTC()
				group.OldestObservedTransactionAt = &stamp
			}
		}
		connectionGroups[key] = group
	}
	for _, group := range connectionGroups {
		report.Connections.Groups = append(report.Connections.Groups, group)
	}
	sort.Slice(report.Connections.Groups, func(i, j int) bool {
		a, b := report.Connections.Groups[i], report.Connections.Groups[j]
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		if a.Build != b.Build {
			return a.Build < b.Build
		}
		return a.State < b.State
	})
	return report
}

func safeRole(role string) ObservedRole {
	if models.LifecycleWriterRole(role).Validate() != nil {
		return "unknown"
	}
	return ObservedRole(role)
}

func parseLabel(label string) (role ObservedRole, build string) {
	parts := strings.Split(label, ":")
	if len(parts) != 3 || parts[0] != "143" || safeRole(parts[1]) == "unknown" {
		return "unknown", "unknown"
	}
	build = parts[2]
	if build == "dev" || build == "unknown" || validHex(build, 40) || (strings.HasPrefix(build, "sha256-") && validHex(strings.TrimPrefix(build, "sha256-"), 32)) {
		return ObservedRole(parts[1]), build
	}
	return "unknown", "unknown"
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func countsScope(truncated bool) CountsScope {
	if truncated {
		return CountsSample
	}
	return CountsObserved
}

func safeState(state *string, visible bool) ActivityState {
	if !visible || state == nil {
		return StateUnknown
	}
	candidate := ActivityState(*state)
	if candidate.Validate() != nil {
		return StateUnknown
	}
	return candidate
}
