package pagerduty

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/ingestion"
	"github.com/google/uuid"
)

const (
	maxPagerDutyRawJSONBytes = 32 * 1024
	maxPagerDutyRawStringLen = 4 * 1024
)

type ParsedEvent struct {
	ProviderEventID string
	EventType       models.PagerDutyEventType
	ResourceType    *string
	OccurredAt      *time.Time
	Incident        Incident
	RawPayload      json.RawMessage
}

type Incident struct {
	ID                   string
	IncidentNumber       *int64
	HTMLURL              *string
	Title                string
	Summary              string
	Status               string
	Urgency              *string
	PriorityID           *string
	PriorityName         *string
	ServiceID            *string
	ServiceName          *string
	EscalationPolicyID   *string
	EscalationPolicyName *string
	IncidentType         *string
	AssignedUserIDs      []string
	TeamIDs              []string
	LatestNote           *string
	TriggeredAt          *time.Time
	AcknowledgedAt       *time.Time
	ResolvedAt           *time.Time
	CreatedAt            *time.Time
	UpdatedAt            *time.Time
	RawData              json.RawMessage
}

type NormalizedEvent struct {
	Issue       ingestion.NormalizedIssue
	IssueStatus models.IssueStatus
	Incident    models.PagerDutyIncident
	Parsed      ParsedEvent
}

func ParseEvent(payload json.RawMessage) (ParsedEvent, error) {
	if len(payload) == 0 {
		return ParsedEvent{}, errors.New("pagerduty payload is empty")
	}
	// Decode with UseNumber so large integer fields (e.g. incident_number)
	// retain full precision and parse identically to the REST sync path
	// (parseAPIIncident). A plain json.Unmarshal would decode every number as
	// float64, truncating values past 2^53 and diverging from sync.
	var root any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return ParsedEvent{}, fmt.Errorf("decode pagerduty payload: %w", err)
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return ParsedEvent{}, errors.New("pagerduty payload must be a JSON object")
	}

	eventMap := findEventMap(rootMap)
	if eventMap == nil {
		return ParsedEvent{}, errors.New("pagerduty payload missing event object")
	}

	eventType := normalizeEventType(eventMap)
	if err := eventType.Validate(); err != nil {
		return ParsedEvent{}, err
	}

	incidentMap := firstMap(eventMap, "data", "incident", "resource")
	if incidentMap == nil {
		incidentMap = firstMap(rootMap, "incident", "data")
	}
	if incidentMap == nil {
		return ParsedEvent{}, errors.New("pagerduty event missing incident data")
	}

	incident, err := parseIncident(incidentMap)
	if err != nil {
		return ParsedEvent{}, err
	}

	occurredAt := parseTime(firstString(eventMap, "occurred_at", "created_at", "timestamp"))
	if occurredAt == nil {
		occurredAt = parseTime(firstString(rootMap, "occurred_at", "created_at", "timestamp"))
	}
	if incident.ID == "" {
		incident.ID = firstString(eventMap, "incident_id", "resource_id")
	}
	if incident.ID == "" {
		return ParsedEvent{}, errors.New("pagerduty incident id is required")
	}

	providerEventID := strings.TrimSpace(firstString(eventMap, "id", "event_id", "webhook_event_id"))
	if providerEventID == "" {
		providerEventID = strings.TrimSpace(firstString(rootMap, "id", "event_id", "webhook_event_id"))
	}
	if providerEventID == "" {
		providerEventID = fmt.Sprintf("%s:%s", eventType, incident.ID)
		if occurredAt != nil {
			providerEventID += ":" + occurredAt.UTC().Format(time.RFC3339Nano)
		}
	}

	resourceType := stringPtrOrNil(firstString(eventMap, "resource_type"))
	return ParsedEvent{
		ProviderEventID: providerEventID,
		EventType:       eventType,
		ResourceType:    resourceType,
		OccurredAt:      occurredAt,
		Incident:        incident,
		RawPayload:      sanitizeRawJSON(payload),
	}, nil
}

func normalizeEventType(eventMap map[string]any) models.PagerDutyEventType {
	raw := strings.TrimSpace(firstString(eventMap, "event_type", "event", "type"))
	resourceType := strings.TrimSpace(firstString(eventMap, "resource_type", "resource_type_name"))
	action := strings.TrimSpace(firstString(eventMap, "action", "event_action", "verb"))
	if raw == "" && strings.EqualFold(resourceType, "incident") && action != "" {
		raw = "incident." + action
	}
	raw = strings.ToLower(strings.ReplaceAll(raw, "-", "_"))
	if strings.HasPrefix(raw, "incident_") {
		raw = "incident." + strings.TrimPrefix(raw, "incident_")
	}
	switch raw {
	case "incident.created":
		raw = string(models.PagerDutyEventIncidentTriggered)
	case "incident.priority.updated":
		raw = string(models.PagerDutyEventIncidentPriorityUpdated)
	case "incident.status_update.published":
		raw = string(models.PagerDutyEventIncidentStatusUpdatePublished)
	}
	return models.PagerDutyEventType(raw)
}

func NormalizeEvent(orgID uuid.UUID, integration models.PagerDutyIntegration, parsed ParsedEvent) (NormalizedEvent, error) {
	if integration.ID == uuid.Nil {
		return NormalizedEvent{}, errors.New("pagerduty integration id is required")
	}
	if integration.IntegrationID == nil || *integration.IntegrationID == uuid.Nil {
		return NormalizedEvent{}, errors.New("generic integration id is required for PagerDuty issue ingestion")
	}
	if parsed.Incident.ID == "" {
		return NormalizedEvent{}, errors.New("pagerduty incident id is required")
	}

	now := time.Now().UTC()
	firstSeen := now
	if parsed.Incident.CreatedAt != nil {
		firstSeen = *parsed.Incident.CreatedAt
	} else if parsed.OccurredAt != nil {
		firstSeen = *parsed.OccurredAt
	}
	lastSeen := firstSeen
	if parsed.OccurredAt != nil {
		lastSeen = *parsed.OccurredAt
	}

	rawData := parsed.Incident.RawData
	if len(rawData) == 0 {
		rawData = parsed.RawPayload
	}
	if len(rawData) == 0 {
		rawData = json.RawMessage(`{}`)
	}

	title := firstNonEmpty(parsed.Incident.Title, parsed.Incident.Summary, parsed.Incident.ID)
	issueStatus := issueStatusForIncident(parsed.EventType, parsed.Incident.Status)
	incident := models.PagerDutyIncident{
		OrgID:                  orgID,
		PagerDutyIntegrationID: integration.ID,
		IncidentID:             parsed.Incident.ID,
		IncidentNumber:         parsed.Incident.IncidentNumber,
		HTMLURL:                parsed.Incident.HTMLURL,
		Title:                  title,
		Status:                 firstNonEmpty(parsed.Incident.Status, statusFromEventType(parsed.EventType)),
		Urgency:                parsed.Incident.Urgency,
		PriorityID:             parsed.Incident.PriorityID,
		PriorityName:           parsed.Incident.PriorityName,
		ServiceID:              parsed.Incident.ServiceID,
		ServiceName:            parsed.Incident.ServiceName,
		EscalationPolicyID:     parsed.Incident.EscalationPolicyID,
		EscalationPolicyName:   parsed.Incident.EscalationPolicyName,
		IncidentType:           parsed.Incident.IncidentType,
		AssignedUserIDs:        parsed.Incident.AssignedUserIDs,
		TeamIDs:                parsed.Incident.TeamIDs,
		LatestNote:             parsed.Incident.LatestNote,
		RawData:                rawData,
		TriggeredAt:            parsed.Incident.TriggeredAt,
		AcknowledgedAt:         parsed.Incident.AcknowledgedAt,
		ResolvedAt:             parsed.Incident.ResolvedAt,
		LastEventAt:            parsed.OccurredAt,
	}
	applyEventTimestamps(parsed.EventType, parsed.OccurredAt, &incident)

	issue := ingestion.NormalizedIssue{
		ExternalID:          parsed.Incident.ID,
		Source:              models.IssueSourcePagerDuty,
		SourceIntegrationID: *integration.IntegrationID,
		Title:               title,
		Description:         incidentDescription(parsed),
		Severity:            issueSeverity(parsed.Incident),
		// The issue upsert ADDS EXCLUDED.occurrence_count on conflict. For
		// PagerDuty one incident maps to one issue, so only a genuine
		// incident.triggered counts as a new occurrence; acknowledged/resolved/
		// status-update re-deliveries and periodic syncs must contribute 0 or
		// they would inflate the count without bound.
		OccurrenceCount:       occurrenceCountForEvent(parsed.EventType, parsed.ProviderEventID),
		AffectedCustomerCount: 0,
		Tags:                  incidentTags(parsed.Incident),
		FirstSeenAt:           firstSeen,
		LastSeenAt:            lastSeen,
		RawData:               rawData,
	}

	return NormalizedEvent{
		Issue:       issue,
		IssueStatus: issueStatus,
		Incident:    incident,
		Parsed:      parsed,
	}, nil
}

func findEventMap(root map[string]any) map[string]any {
	if event := firstMap(root, "event"); event != nil {
		return event
	}
	if messages, ok := root["messages"].([]any); ok && len(messages) > 0 {
		if message, ok := messages[0].(map[string]any); ok {
			return message
		}
	}
	if firstString(root, "event_type", "event", "type") != "" {
		return root
	}
	return nil
}

func parseIncident(m map[string]any) (Incident, error) {
	raw, err := json.Marshal(sanitizeJSONValue(m))
	if err != nil {
		return Incident{}, fmt.Errorf("encode pagerduty incident raw data: %w", err)
	}

	priority := firstMap(m, "priority")
	service := firstMap(m, "service")
	escalationPolicy := firstMap(m, "escalation_policy")

	incident := Incident{
		ID:                   firstString(m, "id", "incident_id"),
		IncidentNumber:       firstInt64(m, "incident_number", "number"),
		HTMLURL:              stringPtrOrNil(firstString(m, "html_url", "url")),
		Title:                firstString(m, "title"),
		Summary:              firstString(m, "summary", "description"),
		Status:               firstString(m, "status"),
		Urgency:              stringPtrOrNil(firstString(m, "urgency")),
		PriorityID:           stringPtrOrNil(firstString(priority, "id")),
		PriorityName:         stringPtrOrNil(firstString(priority, "summary", "name")),
		ServiceID:            stringPtrOrNil(firstString(service, "id")),
		ServiceName:          stringPtrOrNil(firstString(service, "summary", "name")),
		EscalationPolicyID:   stringPtrOrNil(firstString(escalationPolicy, "id")),
		EscalationPolicyName: stringPtrOrNil(firstString(escalationPolicy, "summary", "name")),
		IncidentType:         parseIncidentType(m["incident_type"]),
		AssignedUserIDs:      parseAssignments(m["assignments"]),
		TeamIDs:              parseObjectIDs(m["teams"]),
		LatestNote:           parseLatestNote(m),
		TriggeredAt:          parseTime(firstString(m, "triggered_at", "created_at")),
		AcknowledgedAt:       parseTime(firstString(m, "acknowledged_at")),
		ResolvedAt:           parseTime(firstString(m, "resolved_at")),
		CreatedAt:            parseTime(firstString(m, "created_at")),
		RawData:              raw,
	}
	if incident.Title == "" {
		incident.Title = incident.Summary
	}
	if incident.Status == "" {
		incident.Status = firstString(m, "state")
	}
	return incident, nil
}

func sanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return boundedRawJSON(raw)
	}
	encoded, err := json.Marshal(sanitizeJSONValue(value))
	if err != nil {
		return json.RawMessage(`{"_sanitized":true}`)
	}
	return boundedRawJSON(encoded)
}

func sanitizeJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if pagerDutySensitiveKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = sanitizeJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, sanitizeJSONValue(child))
		}
		return out
	case string:
		if looksLikeSensitivePagerDutyString(typed) {
			return "[REDACTED]"
		}
		if len(typed) > maxPagerDutyRawStringLen {
			return typed[:maxPagerDutyRawStringLen] + "...[TRUNCATED]"
		}
		return typed
	default:
		return typed
	}
}

func boundedRawJSON(raw []byte) json.RawMessage {
	if len(raw) <= maxPagerDutyRawJSONBytes {
		out := make([]byte, len(raw))
		copy(out, raw)
		return json.RawMessage(out)
	}
	encoded, err := json.Marshal(map[string]any{
		"_truncated": true,
		"size_bytes": len(raw),
	})
	if err != nil {
		return json.RawMessage(`{"_truncated":true}`)
	}
	return json.RawMessage(encoded)
}

func pagerDutySensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	for _, marker := range []string{
		"authorization",
		"token",
		"secret",
		"password",
		"api_key",
		"apikey",
		"access_key",
		"refresh",
		"webhook_secret",
		"contact_method",
		"contact_methods",
		"email",
		"phone",
		"address",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func looksLikeSensitivePagerDutyString(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "token ") {
		return true
	}
	// Only redact values that are themselves an email address. Requiring the
	// whole token to be a single, space-free address avoids nuking free-text
	// notes/descriptions (which the agent needs to triage) just because they
	// happen to mention an email or contain an "@".
	return looksLikeEmail(trimmed)
}

// looksLikeEmail reports whether s is a bare email address: no whitespace,
// exactly one "@", non-empty local part, and a dotted domain.
func looksLikeEmail(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') {
		return false
	}
	domain := s[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1
}

func firstMap(m map[string]any, keys ...string) map[string]any {
	if m == nil {
		return nil
	}
	for _, key := range keys {
		if child, ok := m[key].(map[string]any); ok {
			return child
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case json.Number:
			if v.String() != "" {
				return v.String()
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

func firstInt64(m map[string]any, keys ...string) *int64 {
	if m == nil {
		return nil
	}
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			out := int64(v)
			return &out
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return &n
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return &n
			}
		}
	}
	return nil
}

func stringPtrOrNil(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func parseTime(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, v); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func parseIncidentType(v any) *string {
	switch typed := v.(type) {
	case string:
		return stringPtrOrNil(typed)
	case map[string]any:
		return stringPtrOrNil(firstString(typed, "name", "summary", "id"))
	default:
		return nil
	}
}

func parseObjectIDs(v any) []string {
	values, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if m, ok := value.(map[string]any); ok {
			if id := firstString(m, "id"); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func parseAssignments(v any) []string {
	values, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		assignee := firstMap(m, "assignee")
		if id := firstString(assignee, "id"); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func parseLatestNote(m map[string]any) *string {
	for _, key := range []string{"note", "latest_note"} {
		switch note := m[key].(type) {
		case string:
			if out := stringPtrOrNil(note); out != nil {
				return out
			}
		case map[string]any:
			if out := stringPtrOrNil(firstString(note, "content", "summary", "body")); out != nil {
				return out
			}
		}
	}
	if body := firstMap(m, "body"); body != nil {
		return stringPtrOrNil(firstString(body, "details", "content", "summary"))
	}
	return nil
}

// pagerDutySyncEventIDPrefix marks a ParsedEvent that originated from the
// reconciliation sync rather than a live webhook. Sync events must not count as
// new occurrences (they re-observe existing incidents on every poll).
const pagerDutySyncEventIDPrefix = "pagerduty_sync:"

// eventCanTriggerAutomations reports whether an inbound event type may fan out
// to automation runs. incident.annotated and incident.status_update_published
// are excluded because our own writeback (notes and status updates) generates
// exactly those event types — letting them trigger automations would create a
// writeback → webhook → automation → writeback feedback loop.
func eventCanTriggerAutomations(eventType models.PagerDutyEventType) bool {
	switch eventType {
	case models.PagerDutyEventIncidentAnnotated, models.PagerDutyEventIncidentStatusUpdatePublished:
		return false
	default:
		return true
	}
}

// occurrenceCountForEvent returns the occurrence increment for an event. Only a
// genuine incident.triggered webhook represents a new occurrence; every other
// event type, and any reconciliation-sync event, returns 0 so the running
// occurrence_count is not inflated by status churn or periodic polling.
func occurrenceCountForEvent(eventType models.PagerDutyEventType, providerEventID string) int {
	if eventType == models.PagerDutyEventIncidentTriggered && !strings.HasPrefix(providerEventID, pagerDutySyncEventIDPrefix) {
		return 1
	}
	return 0
}

func issueStatusForIncident(eventType models.PagerDutyEventType, status string) models.IssueStatus {
	if eventType == models.PagerDutyEventIncidentResolved || strings.EqualFold(status, "resolved") {
		return models.IssueStatusFixed
	}
	return models.IssueStatusOpen
}

// IssueStatusForIncidentStatus maps the authoritative (recency-merged) incident
// status to an issue status. Callers should prefer this over deriving status
// from a single event so that out-of-order webhook deliveries cannot reopen a
// resolved issue.
func IssueStatusForIncidentStatus(status string) models.IssueStatus {
	if strings.EqualFold(strings.TrimSpace(status), "resolved") {
		return models.IssueStatusFixed
	}
	return models.IssueStatusOpen
}

func statusFromEventType(eventType models.PagerDutyEventType) string {
	raw := strings.TrimPrefix(string(eventType), "incident.")
	switch raw {
	case "priority_updated", "status_update_published":
		return ""
	default:
		return raw
	}
}

func issueSeverity(incident Incident) string {
	priority := strings.ToUpper(strings.TrimSpace(ptrValue(incident.PriorityName)))
	priority = strings.ReplaceAll(priority, " ", "")
	switch {
	case priority == "P1" || priority == "SEV1" || strings.Contains(priority, "CRITICAL"):
		return "critical"
	case priority == "P2" || priority == "SEV2" || strings.Contains(priority, "HIGH"):
		return "high"
	case priority == "P3" || priority == "SEV3" || strings.Contains(priority, "MEDIUM"):
		return "medium"
	case priority == "P4" || priority == "P5" || strings.Contains(priority, "LOW"):
		return "low"
	}

	switch strings.ToLower(strings.TrimSpace(ptrValue(incident.Urgency))) {
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

func incidentDescription(parsed ParsedEvent) string {
	incident := parsed.Incident
	var b strings.Builder
	if incident.Summary != "" && incident.Summary != incident.Title {
		b.WriteString(incident.Summary)
	} else {
		b.WriteString(firstNonEmpty(incident.Title, incident.ID))
	}
	writeDescriptionField(&b, "Status", incident.Status)
	writeDescriptionField(&b, "Urgency", ptrValue(incident.Urgency))
	writeDescriptionField(&b, "Priority", ptrValue(incident.PriorityName))
	writeDescriptionField(&b, "Service", ptrValue(incident.ServiceName))
	writeDescriptionField(&b, "Escalation policy", ptrValue(incident.EscalationPolicyName))
	writeDescriptionField(&b, "Incident type", ptrValue(incident.IncidentType))
	writeDescriptionField(&b, "Incident URL", ptrValue(incident.HTMLURL))
	if parsed.OccurredAt != nil {
		writeDescriptionField(&b, "Event time", parsed.OccurredAt.Format(time.RFC3339))
	}
	if incident.LatestNote != nil && strings.TrimSpace(*incident.LatestNote) != "" {
		b.WriteString("\n\nLatest note:\n")
		b.WriteString(strings.TrimSpace(*incident.LatestNote))
	}
	return strings.TrimSpace(b.String())
}

func writeDescriptionField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(value)
}

func incidentTags(incident Incident) []string {
	seen := map[string]bool{}
	tags := make([]string, 0, 8)
	add := func(tag string) {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			return
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	add("pagerduty")
	add("pagerduty_service:" + ptrValue(incident.ServiceName))
	add("pagerduty_service_id:" + ptrValue(incident.ServiceID))
	add("pagerduty_priority:" + ptrValue(incident.PriorityName))
	add("pagerduty_urgency:" + ptrValue(incident.Urgency))
	add("pagerduty_escalation_policy:" + ptrValue(incident.EscalationPolicyName))
	add("pagerduty_incident_type:" + ptrValue(incident.IncidentType))
	for _, teamID := range incident.TeamIDs {
		add("pagerduty_team_id:" + teamID)
	}
	sort.Strings(tags[1:])
	return tags
}

func applyEventTimestamps(eventType models.PagerDutyEventType, occurredAt *time.Time, incident *models.PagerDutyIncident) {
	if occurredAt == nil {
		return
	}
	switch eventType {
	case models.PagerDutyEventIncidentTriggered, models.PagerDutyEventIncidentReopened:
		if incident.TriggeredAt == nil {
			incident.TriggeredAt = occurredAt
		}
	case models.PagerDutyEventIncidentAcknowledged:
		if incident.AcknowledgedAt == nil {
			incident.AcknowledgedAt = occurredAt
		}
	case models.PagerDutyEventIncidentResolved:
		if incident.ResolvedAt == nil {
			incident.ResolvedAt = occurredAt
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ptrValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
