package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PagerDutyIncidentProvider struct {
	httpClient       *http.Client
	baseURL          string
	authToken        string
	writebackEnabled bool
}

type PagerDutyProviderConfig struct {
	AccessToken      string
	BaseURL          string
	WritebackEnabled bool
}

func NewPagerDutyIncidentProvider(cfg PagerDutyProviderConfig) *PagerDutyIncidentProvider {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.pagerduty.com"
	}
	return &PagerDutyIncidentProvider{
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		baseURL:          baseURL,
		authToken:        cfg.AccessToken,
		writebackEnabled: cfg.WritebackEnabled,
	}
}

func (p *PagerDutyIncidentProvider) Name() string { return "pagerduty" }

func (p *PagerDutyIncidentProvider) WritebackEnabled() bool {
	return p != nil && p.writebackEnabled
}

func (p *PagerDutyIncidentProvider) ListIncidents(ctx context.Context, filter IncidentFilter) ([]IncidentSummary, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 25
	}
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	if len(filter.Statuses) == 0 {
		values.Add("statuses[]", "triggered")
		values.Add("statuses[]", "acknowledged")
	} else {
		for _, status := range filter.Statuses {
			if status = strings.TrimSpace(status); status != "" {
				values.Add("statuses[]", status)
			}
		}
	}
	if filter.Urgency != "" {
		values.Add("urgencies[]", strings.TrimSpace(filter.Urgency))
	}
	if filter.Service != "" {
		values.Add("service_ids[]", strings.TrimSpace(filter.Service))
	}
	if !filter.Since.IsZero() {
		values.Set("since", filter.Since.Format(time.RFC3339))
	}

	var response struct {
		Incidents []pagerDutyAPIIncident `json:"incidents"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/incidents?"+values.Encode(), nil, &response); err != nil {
		return nil, fmt.Errorf("list pagerduty incidents: %w", err)
	}
	out := make([]IncidentSummary, 0, len(response.Incidents))
	for _, incident := range response.Incidents {
		out = append(out, pagerDutyIncidentSummary(incident))
	}
	return out, nil
}

func (p *PagerDutyIncidentProvider) GetIncident(ctx context.Context, incidentID string) (*IncidentDetail, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident_id is required")
	}
	var response struct {
		Incident pagerDutyAPIIncident `json:"incident"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/incidents/"+url.PathEscape(incidentID), nil, &response); err != nil {
		return nil, fmt.Errorf("get pagerduty incident: %w", err)
	}
	summary := pagerDutyIncidentSummary(response.Incident)
	return &IncidentDetail{
		IncidentSummary:  summary,
		Description:      response.Incident.Description,
		EscalationPolicy: response.Incident.EscalationPolicy.Summary,
		AssignedUserNames: pagerDutyAssignments(
			response.Incident.Assignments,
		),
		Teams: pagerDutyTeams(response.Incident.Teams),
	}, nil
}

func (p *PagerDutyIncidentProvider) AddIncidentNote(ctx context.Context, incidentID, note string) (string, error) {
	incidentID = strings.TrimSpace(incidentID)
	note = strings.TrimSpace(note)
	if incidentID == "" {
		return "", fmt.Errorf("incident_id is required")
	}
	if note == "" {
		return "", fmt.Errorf("note is required")
	}
	if err := p.requireWritebackEnabled(); err != nil {
		return "", err
	}
	body := map[string]any{"note": map[string]string{"content": note}}
	var response struct {
		Note struct {
			ID string `json:"id"`
		} `json:"note"`
	}
	if err := p.do(ctx, http.MethodPost, p.baseURL+"/incidents/"+url.PathEscape(incidentID)+"/notes", body, &response); err != nil {
		return "", fmt.Errorf("add pagerduty incident note: %w", err)
	}
	return strings.TrimSpace(response.Note.ID), nil
}

func (p *PagerDutyIncidentProvider) ListIncidentNotes(ctx context.Context, incidentID string, limit int) ([]IncidentNote, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident_id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	var response struct {
		Notes []pagerDutyAPINote `json:"notes"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/incidents/"+url.PathEscape(incidentID)+"/notes?"+values.Encode(), nil, &response); err != nil {
		return nil, fmt.Errorf("list pagerduty incident notes: %w", err)
	}
	out := make([]IncidentNote, 0, len(response.Notes))
	for _, note := range response.Notes {
		out = append(out, IncidentNote{
			ID:         note.ID,
			IncidentID: incidentID,
			Content:    note.Content,
			UserID:     note.User.ID,
			UserName:   note.User.Summary,
			CreatedAt:  note.CreatedAt,
		})
	}
	return out, nil
}

func (p *PagerDutyIncidentProvider) ListIncidentLogEntries(ctx context.Context, incidentID string, limit int) ([]IncidentLogEntry, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, fmt.Errorf("incident_id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	var response struct {
		LogEntries []pagerDutyAPILogEntry `json:"log_entries"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/incidents/"+url.PathEscape(incidentID)+"/log_entries?"+values.Encode(), nil, &response); err != nil {
		return nil, fmt.Errorf("list pagerduty incident log entries: %w", err)
	}
	out := make([]IncidentLogEntry, 0, len(response.LogEntries))
	for _, entry := range response.LogEntries {
		out = append(out, IncidentLogEntry{
			ID:         entry.ID,
			IncidentID: incidentID,
			Type:       entry.Type,
			Summary:    entry.Summary,
			AgentID:    entry.Agent.ID,
			AgentName:  entry.Agent.Summary,
			CreatedAt:  entry.CreatedAt,
		})
	}
	return out, nil
}

func (p *PagerDutyIncidentProvider) GetService(ctx context.Context, serviceID string) (*IncidentService, error) {
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		return nil, fmt.Errorf("service_id is required")
	}
	var response struct {
		Service pagerDutyAPIService `json:"service"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/services/"+url.PathEscape(serviceID), nil, &response); err != nil {
		return nil, fmt.Errorf("get pagerduty service: %w", err)
	}
	return &IncidentService{
		ID:               response.Service.ID,
		Name:             firstNonEmptyPagerDuty(response.Service.Name, response.Service.Summary),
		Description:      response.Service.Description,
		HTMLURL:          response.Service.HTMLURL,
		EscalationPolicy: response.Service.EscalationPolicy.Summary,
		TeamIDs:          pagerDutyReferenceIDs(response.Service.Teams),
	}, nil
}

func (p *PagerDutyIncidentProvider) ListOnCalls(ctx context.Context, filter OnCallFilter) ([]OnCall, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	values := url.Values{}
	values.Set("limit", fmt.Sprintf("%d", limit))
	if scheduleID := strings.TrimSpace(filter.ScheduleID); scheduleID != "" {
		values.Add("schedule_ids[]", scheduleID)
	}
	var response struct {
		OnCalls []pagerDutyAPIOnCall `json:"oncalls"`
	}
	if err := p.do(ctx, http.MethodGet, p.baseURL+"/oncalls?"+values.Encode(), nil, &response); err != nil {
		return nil, fmt.Errorf("list pagerduty on-calls: %w", err)
	}
	out := make([]OnCall, 0, len(response.OnCalls))
	for _, onCall := range response.OnCalls {
		out = append(out, OnCall{
			UserID:               onCall.User.ID,
			UserName:             onCall.User.Summary,
			ScheduleID:           onCall.Schedule.ID,
			ScheduleName:         onCall.Schedule.Summary,
			EscalationPolicyID:   onCall.EscalationPolicy.ID,
			EscalationPolicyName: onCall.EscalationPolicy.Summary,
			Start:                onCall.Start,
			End:                  onCall.End,
		})
	}
	return out, nil
}

func (p *PagerDutyIncidentProvider) FindRelatedIncidents(ctx context.Context, incidentID string, days int) ([]IncidentSummary, error) {
	incident, err := p.GetIncident(ctx, incidentID)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 90
	}
	related, err := p.ListIncidents(ctx, IncidentFilter{
		Statuses: []string{"triggered", "acknowledged", "resolved"},
		Service:  incident.ServiceID,
		Since:    time.Now().Add(-time.Duration(days) * 24 * time.Hour),
		Limit:    50,
	})
	if err != nil {
		return nil, fmt.Errorf("find related pagerduty incidents: %w", err)
	}
	out := make([]IncidentSummary, 0, len(related))
	for _, candidate := range related {
		if candidate.ID == incidentID {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (p *PagerDutyIncidentProvider) CreateIncidentStatusUpdate(ctx context.Context, incidentID, body string) error {
	incidentID = strings.TrimSpace(incidentID)
	body = strings.TrimSpace(body)
	if incidentID == "" {
		return fmt.Errorf("incident_id is required")
	}
	if body == "" {
		return fmt.Errorf("body is required")
	}
	if err := p.requireWritebackEnabled(); err != nil {
		return err
	}
	payload := map[string]string{"message": body}
	if err := p.do(ctx, http.MethodPost, p.baseURL+"/incidents/"+url.PathEscape(incidentID)+"/status_updates", payload, nil); err != nil {
		return fmt.Errorf("create pagerduty incident status update: %w", err)
	}
	return nil
}

func (p *PagerDutyIncidentProvider) requireWritebackEnabled() error {
	if p != nil && p.writebackEnabled {
		return nil
	}
	return fmt.Errorf("PagerDuty writeback is disabled")
}

func (p *PagerDutyIncidentProvider) do(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")
	req.Header.Set("Authorization", "Bearer "+p.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if len(raw) > 0 {
			return fmt.Errorf("pagerduty API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return fmt.Errorf("pagerduty API returned %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type pagerDutyAPIIncident struct {
	ID               string                  `json:"id"`
	IncidentNumber   int64                   `json:"incident_number"`
	Title            string                  `json:"title"`
	Description      string                  `json:"description"`
	Status           string                  `json:"status"`
	Urgency          string                  `json:"urgency"`
	HTMLURL          string                  `json:"html_url"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	Service          pagerDutyAPIReference   `json:"service"`
	Priority         pagerDutyAPIReference   `json:"priority"`
	EscalationPolicy pagerDutyAPIReference   `json:"escalation_policy"`
	Assignments      []pagerDutyAssignment   `json:"assignments"`
	Teams            []pagerDutyAPIReference `json:"teams"`
}

type pagerDutyAPIReference struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type pagerDutyAssignment struct {
	Assignee pagerDutyAPIReference `json:"assignee"`
}

type pagerDutyAPINote struct {
	ID        string                `json:"id"`
	Content   string                `json:"content"`
	User      pagerDutyAPIReference `json:"user"`
	CreatedAt time.Time             `json:"created_at"`
}

type pagerDutyAPILogEntry struct {
	ID        string                `json:"id"`
	Type      string                `json:"type"`
	Summary   string                `json:"summary"`
	Agent     pagerDutyAPIReference `json:"agent"`
	CreatedAt time.Time             `json:"created_at"`
}

type pagerDutyAPIService struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Summary          string                  `json:"summary"`
	Description      string                  `json:"description"`
	HTMLURL          string                  `json:"html_url"`
	EscalationPolicy pagerDutyAPIReference   `json:"escalation_policy"`
	Teams            []pagerDutyAPIReference `json:"teams"`
}

type pagerDutyAPIOnCall struct {
	User             pagerDutyAPIReference `json:"user"`
	Schedule         pagerDutyAPIReference `json:"schedule"`
	EscalationPolicy pagerDutyAPIReference `json:"escalation_policy"`
	Start            time.Time             `json:"start"`
	End              time.Time             `json:"end"`
}

func pagerDutyIncidentSummary(incident pagerDutyAPIIncident) IncidentSummary {
	title := incident.Title
	if title == "" {
		title = incident.Description
	}
	return IncidentSummary{
		ID:          incident.ID,
		Number:      incident.IncidentNumber,
		Title:       title,
		Status:      incident.Status,
		Urgency:     incident.Urgency,
		Priority:    incident.Priority.Summary,
		ServiceID:   incident.Service.ID,
		ServiceName: incident.Service.Summary,
		CreatedAt:   incident.CreatedAt,
		UpdatedAt:   incident.UpdatedAt,
		WebURL:      incident.HTMLURL,
	}
}

func pagerDutyAssignments(assignments []pagerDutyAssignment) []string {
	out := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.Assignee.Summary != "" {
			out = append(out, assignment.Assignee.Summary)
		}
	}
	return out
}

func pagerDutyTeams(teams []pagerDutyAPIReference) []string {
	out := make([]string, 0, len(teams))
	for _, team := range teams {
		if team.Summary != "" {
			out = append(out, team.Summary)
		}
	}
	return out
}

func pagerDutyReferenceIDs(refs []pagerDutyAPIReference) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.ID != "" {
			out = append(out, ref.ID)
		}
	}
	return out
}

func firstNonEmptyPagerDuty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
