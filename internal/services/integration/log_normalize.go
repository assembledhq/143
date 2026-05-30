package integration

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
)

const (
	logEntryMessageLimit = 10 * 1024
	logEntryFieldLimit   = 1024
)

func normalizeLogRecords(provider models.ProviderName, records []map[string]any, fields []string, includeRaw *bool) []LogEntry {
	entries := make([]LogEntry, 0, len(records))
	fieldSet := make(map[string]bool, len(fields))
	for _, field := range fields {
		fieldSet[field] = true
	}
	for _, record := range records {
		redacted := RedactLogPayload(record)
		entry := LogEntry{
			ID:        stringFromLogRecord(redacted, "id", "_id", "event_id"),
			Timestamp: timeFromLogRecord(redacted, "timestamp", "_time", "time", "created_at"),
			Provider:  provider,
			Message:   truncateString(stringFromLogRecord(redacted, "message", "_msg", "msg", "text"), logEntryMessageLimit),
			Level:     stringFromLogRecord(redacted, "level", "severity"),
			Service:   stringFromLogRecord(redacted, "service", "service_name"),
			TraceID:   stringFromLogRecord(redacted, "trace_id", "traceID"),
			RequestID: stringFromLogRecord(redacted, "request_id", "requestID"),
			OrgID:     stringFromLogRecord(redacted, "org_id", "orgID"),
			Fields:    filteredLogFields(redacted, fieldSet),
		}
		if includeRaw != nil && *includeRaw {
			entry.Raw = truncateRawLogPayload(redacted)
		}
		entries = append(entries, entry)
	}
	return entries
}

func filteredLogFields(record map[string]any, fieldSet map[string]bool) map[string]any {
	out := make(map[string]any)
	for key, value := range record {
		if normalizedLogField(key) {
			continue
		}
		if len(fieldSet) > 0 && !fieldSet[key] {
			continue
		}
		out[key] = truncateLogFieldValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizedLogField(key string) bool {
	switch strings.ToLower(key) {
	case "id", "_id", "event_id", "timestamp", "_time", "time", "created_at", "message", "_msg", "msg", "text", "level", "severity", "service", "service_name", "trace_id", "traceid", "request_id", "requestid", "org_id", "orgid":
		return true
	default:
		return false
	}
}

func truncateLogFieldValue(value any) any {
	switch v := value.(type) {
	case string:
		return truncateString(v, logEntryFieldLimit)
	case map[string]any, []any:
		data, err := json.Marshal(v)
		if err != nil {
			return v
		}
		return truncateString(string(data), logEntryFieldLimit)
	default:
		return value
	}
}

func truncateRawLogPayload(record map[string]any) map[string]any {
	out := make(map[string]any, len(record))
	for key, value := range record {
		out[key] = truncateRawLogValue(value)
	}
	return out
}

func truncateRawLogValue(value any) any {
	switch v := value.(type) {
	case string:
		return truncateString(v, logEntryFieldLimit)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = truncateRawLogValue(child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = truncateRawLogValue(child)
		}
		return out
	default:
		return value
	}
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func stringFromLogRecord(record map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			return v
		case fmt.Stringer:
			return v.String()
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case int:
			return strconv.Itoa(v)
		}
	}
	return ""
}

func timeFromLogRecord(record map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return parsed
			}
		case float64:
			if v > 1e12 {
				return time.UnixMilli(int64(v)).UTC()
			}
			return time.Unix(int64(v), 0).UTC()
		}
	}
	return time.Time{}
}

func trimEntries(entries []LogEntry, limit int) ([]LogEntry, bool) {
	if limit <= 0 || len(entries) <= limit {
		return entries, false
	}
	return entries[:limit], true
}

func sortLogEntries(entries []LogEntry, direction LogDirection) {
	sort.SliceStable(entries, func(i, j int) bool {
		if direction == LogDirectionAsc {
			return entries[i].Timestamp.Before(entries[j].Timestamp)
		}
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
}

func splitContextEntries(entries []LogEntry, timestamp time.Time, beforeCount int, afterCount int) ([]LogEntry, *LogEntry, []LogEntry) {
	if len(entries) == 0 {
		return nil, nil, nil
	}
	targetIndex := 0
	bestDistance := absDuration(entries[0].Timestamp.Sub(timestamp))
	for i := 1; i < len(entries); i++ {
		distance := absDuration(entries[i].Timestamp.Sub(timestamp))
		if distance < bestDistance {
			bestDistance = distance
			targetIndex = i
		}
	}
	beforeStart := max(0, targetIndex-beforeCount)
	afterEnd := min(len(entries), targetIndex+afterCount+1)
	target := entries[targetIndex]
	return entries[beforeStart:targetIndex], &target, entries[targetIndex+1 : afterEnd]
}

func collectLogFields(entries []LogEntry, limit int) []LogField {
	seen := make(map[string]LogField)
	for _, entry := range entries {
		record := map[string]any{
			"id":         entry.ID,
			"timestamp":  entry.Timestamp,
			"message":    entry.Message,
			"level":      entry.Level,
			"service":    entry.Service,
			"trace_id":   entry.TraceID,
			"request_id": entry.RequestID,
			"org_id":     entry.OrgID,
		}
		for key, value := range entry.Fields {
			record[key] = value
		}
		for key, value := range record {
			if value == "" || value == nil {
				continue
			}
			field := seen[key]
			field.Name = key
			field.Type = logFieldType(value)
			if len(field.SampleValues) < 3 && !slices.ContainsFunc(field.SampleValues, func(sample any) bool {
				return fmt.Sprint(sample) == fmt.Sprint(value)
			}) {
				field.SampleValues = append(field.SampleValues, value)
			}
			seen[key] = field
		}
	}
	fields := make([]LogField, 0, len(seen))
	for _, field := range seen {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	if limit > 0 && len(fields) > limit {
		return fields[:limit]
	}
	return fields
}

func statsFromRecords(records []map[string]any, groupBy []string) []LogStatsSeries {
	if len(records) == 0 {
		return nil
	}
	series := make([]LogStatsSeries, 0, len(records))
	for _, record := range records {
		group := make(map[string]string)
		for _, key := range groupBy {
			if value, ok := record[key]; ok {
				group[key] = fmt.Sprint(value)
			}
		}
		count := 0
		for _, key := range []string{"count", "hits", "_hits"} {
			if value, ok := record[key]; ok {
				count = intFromAny(value)
				break
			}
		}
		ts := timeFromLogRecord(record, "timestamp", "_time", "time")
		series = append(series, LogStatsSeries{Group: group, Buckets: []LogStatsBucket{{Timestamp: ts, Count: count}}})
	}
	return series
}

func logFieldType(value any) string {
	switch value.(type) {
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	case time.Time:
		return "timestamp"
	default:
		return "string"
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

func intValue(value *int, fallback int) int {
	if value == nil || *value == 0 {
		return fallback
	}
	return *value
}

func ptr[T any](value T) *T {
	return &value
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
