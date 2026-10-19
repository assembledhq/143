package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCodeReviewRiskTierCheckName = "risk-tier"
	CodeReviewRiskTierPayloadFence     = "```json pr-risk"
	codeReviewRiskTierPayloadClose     = "```"
	codeReviewRiskTierSchemaVersion    = 1
	maxCodeReviewRiskTierCheckName     = 255
)

var (
	codeReviewGitHubAppSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	codeReviewRiskTierSHAPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	codeReviewRiskTierHashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CodeReviewRiskTierCheckPolicy binds the code review size safeguard and
// publisher_result description applicability to the risk-tier check run that
// one GitHub App publishes for the assessed head. Runs are matched by check
// name and numeric App ID; AppSlug is display-only. Only enforce-mode payloads
// are authoritative. When several authoritative runs exist for the head, or a
// checkpoint is combined with a live read, the most restrictive result wins:
// tier high > unavailable > unscored > medium > low, and ui_render_change
// true > unknown > false.
type CodeReviewRiskTierCheckPolicy struct {
	Enabled   bool   `json:"enabled"`
	AppID     int64  `json:"app_id,omitempty"`
	AppSlug   string `json:"app_slug,omitempty"`
	CheckName string `json:"check_name,omitempty"`
}

func (p *CodeReviewRiskTierCheckPolicy) Active() bool {
	return p != nil && p.Enabled
}

func (p *CodeReviewRiskTierCheckPolicy) EffectiveCheckName() string {
	if p == nil || p.CheckName == "" {
		return DefaultCodeReviewRiskTierCheckName
	}
	return p.CheckName
}

func (p *CodeReviewRiskTierCheckPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.AppID < 0 {
		return errors.New("risk_tier_check.app_id must be a positive GitHub App ID")
	}
	if p.Enabled && p.AppID == 0 {
		return errors.New("risk_tier_check.app_id is required when risk_tier_check is enabled")
	}
	if p.AppSlug != "" && !codeReviewGitHubAppSlugPattern.MatchString(p.AppSlug) {
		return errors.New("risk_tier_check.app_slug must be a GitHub App slug of lowercase letters, digits, and single hyphens")
	}
	if p.CheckName != "" && (strings.TrimSpace(p.CheckName) != p.CheckName || len(p.CheckName) > maxCodeReviewRiskTierCheckName) {
		return fmt.Errorf("risk_tier_check.check_name must be an exact check name without surrounding whitespace and at most %d bytes", maxCodeReviewRiskTierCheckName)
	}
	return nil
}

func ValidateCodeReviewPolicyConfigFields(raw json.RawMessage) error {
	var config struct {
		RiskPolicy json.RawMessage `json:"risk_policy"`
	}
	if err := json.Unmarshal(raw, &config); err != nil || codeReviewJSONAbsent(config.RiskPolicy) {
		return nil
	}
	var risk struct {
		RiskTierCheck json.RawMessage `json:"risk_tier_check"`
	}
	if err := json.Unmarshal(config.RiskPolicy, &risk); err != nil || codeReviewJSONAbsent(risk.RiskTierCheck) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(risk.RiskTierCheck))
	decoder.DisallowUnknownFields()
	var check CodeReviewRiskTierCheckPolicy
	if err := decoder.Decode(&check); err != nil {
		return codeReviewPolicyFieldError(CodeReviewPolicyFieldRiskPolicy, "risk_tier_check: "+err.Error())
	}
	return nil
}

func codeReviewJSONAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

type CodeReviewRiskTier string

const (
	CodeReviewRiskTierLow         CodeReviewRiskTier = "low"
	CodeReviewRiskTierMedium      CodeReviewRiskTier = "medium"
	CodeReviewRiskTierHigh        CodeReviewRiskTier = "high"
	CodeReviewRiskTierUnavailable CodeReviewRiskTier = "unavailable"
	CodeReviewRiskTierUnscored    CodeReviewRiskTier = "unscored"
)

func (t CodeReviewRiskTier) Validate() error {
	switch t {
	case CodeReviewRiskTierLow, CodeReviewRiskTierMedium, CodeReviewRiskTierHigh, CodeReviewRiskTierUnavailable, CodeReviewRiskTierUnscored:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewRiskTier: %q", t)
	}
}

type CodeReviewRiskTierGate string

const (
	CodeReviewRiskTierGateSuccess        CodeReviewRiskTierGate = "success"
	CodeReviewRiskTierGateActionRequired CodeReviewRiskTierGate = "action_required"
	CodeReviewRiskTierGateNeutral        CodeReviewRiskTierGate = "neutral"
)

func (g CodeReviewRiskTierGate) Validate() error {
	switch g {
	case CodeReviewRiskTierGateSuccess, CodeReviewRiskTierGateActionRequired, CodeReviewRiskTierGateNeutral:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewRiskTierGate: %q", g)
	}
}

type CodeReviewUIRenderChange string

const (
	CodeReviewUIRenderChangeTrue    CodeReviewUIRenderChange = "true"
	CodeReviewUIRenderChangeFalse   CodeReviewUIRenderChange = "false"
	CodeReviewUIRenderChangeUnknown CodeReviewUIRenderChange = "unknown"
)

func (c CodeReviewUIRenderChange) Validate() error {
	switch c {
	case CodeReviewUIRenderChangeTrue, CodeReviewUIRenderChangeFalse, CodeReviewUIRenderChangeUnknown:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewUIRenderChange: %q", c)
	}
}

type CodeReviewRiskTierMode string

const (
	CodeReviewRiskTierModeShadow  CodeReviewRiskTierMode = "shadow"
	CodeReviewRiskTierModeEnforce CodeReviewRiskTierMode = "enforce"
)

func (m CodeReviewRiskTierMode) Validate() error {
	switch m {
	case CodeReviewRiskTierModeShadow, CodeReviewRiskTierModeEnforce:
		return nil
	default:
		return fmt.Errorf("invalid CodeReviewRiskTierMode: %q", m)
	}
}

type CodeReviewRiskTierGeneration struct {
	PR           int64                  `json:"pr"`
	HeadSHA      string                 `json:"head_sha"`
	BaseSHA      string                 `json:"base_sha"`
	MergeBaseSHA string                 `json:"merge_base_sha"`
	TrunkSHA     string                 `json:"trunk_sha"`
	IndexID      *string                `json:"index_id"`
	PolicyHash   string                 `json:"policy_hash"`
	Mode         CodeReviewRiskTierMode `json:"mode"`
}

type CodeReviewRiskTierPayload struct {
	SchemaVersion  int                          `json:"schema_version"`
	Generation     CodeReviewRiskTierGeneration `json:"generation"`
	Tier           CodeReviewRiskTier           `json:"tier"`
	Gate           CodeReviewRiskTierGate       `json:"gate"`
	Override       bool                         `json:"override"`
	UIRenderChange CodeReviewUIRenderChange     `json:"ui_render_change"`
	PublishedAt    time.Time                    `json:"published_at"`
}

var (
	codeReviewRiskTierPayloadKeys    = []string{"schema_version", "generation", "tier", "gate", "override", "ui_render_change", "published_at"}
	codeReviewRiskTierGenerationKeys = []string{"pr", "head_sha", "base_sha", "merge_base_sha", "trunk_sha", "index_id", "policy_hash", "mode"}
)

func ExtractCodeReviewRiskTierPayload(checkOutputText string) (CodeReviewRiskTierPayload, error) {
	lines := strings.Split(checkOutputText, "\n")
	var body []string
	blocks := 0
	for i := 0; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") != CodeReviewRiskTierPayloadFence {
			continue
		}
		blocks++
		if blocks > 1 {
			return CodeReviewRiskTierPayload{}, errors.New("risk-tier output contains more than one payload block")
		}
		closed := false
		for i++; i < len(lines); i++ {
			if strings.TrimSuffix(lines[i], "\r") == codeReviewRiskTierPayloadClose {
				closed = true
				break
			}
			body = append(body, lines[i])
		}
		if !closed {
			return CodeReviewRiskTierPayload{}, errors.New("risk-tier payload fence is not closed")
		}
	}
	if blocks == 0 {
		return CodeReviewRiskTierPayload{}, errors.New("risk-tier payload fence not found")
	}
	return ParseCodeReviewRiskTierPayload([]byte(strings.Join(body, "\n")))
}

func ParseCodeReviewRiskTierPayload(raw []byte) (CodeReviewRiskTierPayload, error) {
	var payload CodeReviewRiskTierPayload
	fields, err := strictCodeReviewRiskTierObject(raw, codeReviewRiskTierPayloadKeys)
	if err != nil {
		return payload, fmt.Errorf("risk-tier payload: %w", err)
	}
	version, err := codeReviewRiskTierInteger(fields["schema_version"])
	if err != nil || version != codeReviewRiskTierSchemaVersion {
		return payload, fmt.Errorf("risk-tier payload: schema_version must be %d", codeReviewRiskTierSchemaVersion)
	}
	payload.SchemaVersion = codeReviewRiskTierSchemaVersion
	if payload.Generation, err = parseCodeReviewRiskTierGeneration(fields["generation"]); err != nil {
		return payload, fmt.Errorf("risk-tier payload: %w", err)
	}
	var tier, gate, renderChange, publishedAt string
	for _, field := range []struct {
		key    string
		target *string
	}{{"tier", &tier}, {"gate", &gate}, {"ui_render_change", &renderChange}, {"published_at", &publishedAt}} {
		if *field.target, err = codeReviewRiskTierString(fields[field.key]); err != nil {
			return payload, fmt.Errorf("risk-tier payload: %s: %w", field.key, err)
		}
	}
	payload.Tier, payload.Gate, payload.UIRenderChange = CodeReviewRiskTier(tier), CodeReviewRiskTierGate(gate), CodeReviewUIRenderChange(renderChange)
	for _, validate := range []func() error{payload.Tier.Validate, payload.Gate.Validate, payload.UIRenderChange.Validate} {
		if err := validate(); err != nil {
			return payload, fmt.Errorf("risk-tier payload: %w", err)
		}
	}
	override := bytes.TrimSpace(fields["override"])
	if !bytes.Equal(override, []byte("true")) && !bytes.Equal(override, []byte("false")) {
		return payload, errors.New("risk-tier payload: override must be a boolean")
	}
	payload.Override = bytes.Equal(override, []byte("true"))
	if payload.PublishedAt, err = time.Parse(time.RFC3339Nano, publishedAt); err != nil {
		return payload, errors.New("risk-tier payload: published_at must be an RFC 3339 date-time")
	}
	return payload, nil
}

func parseCodeReviewRiskTierGeneration(raw json.RawMessage) (CodeReviewRiskTierGeneration, error) {
	var generation CodeReviewRiskTierGeneration
	fields, err := strictCodeReviewRiskTierObject(raw, codeReviewRiskTierGenerationKeys)
	if err != nil {
		return generation, fmt.Errorf("generation: %w", err)
	}
	if generation.PR, err = codeReviewRiskTierInteger(fields["pr"]); err != nil || generation.PR < 1 {
		return generation, errors.New("generation.pr must be a positive integer")
	}
	for _, field := range []struct {
		key    string
		target *string
	}{{"head_sha", &generation.HeadSHA}, {"base_sha", &generation.BaseSHA}, {"merge_base_sha", &generation.MergeBaseSHA}, {"trunk_sha", &generation.TrunkSHA}} {
		if *field.target, err = codeReviewRiskTierString(fields[field.key]); err != nil || !codeReviewRiskTierSHAPattern.MatchString(*field.target) {
			return generation, fmt.Errorf("generation.%s must be a full lowercase commit SHA", field.key)
		}
	}
	if generation.PolicyHash, err = codeReviewRiskTierString(fields["policy_hash"]); err != nil || !codeReviewRiskTierHashPattern.MatchString(generation.PolicyHash) {
		return generation, errors.New("generation.policy_hash must be a lowercase SHA-256 digest")
	}
	if !bytes.Equal(bytes.TrimSpace(fields["index_id"]), []byte("null")) {
		indexID, err := codeReviewRiskTierString(fields["index_id"])
		if err != nil || !codeReviewRiskTierHashPattern.MatchString(indexID) {
			return generation, errors.New("generation.index_id must be null or a lowercase SHA-256 digest")
		}
		generation.IndexID = &indexID
	}
	mode, err := codeReviewRiskTierString(fields["mode"])
	if err != nil {
		return generation, fmt.Errorf("generation.mode: %w", err)
	}
	generation.Mode = CodeReviewRiskTierMode(mode)
	if err := generation.Mode.Validate(); err != nil {
		return generation, fmt.Errorf("generation.mode: %w", err)
	}
	return generation, nil
}

func strictCodeReviewRiskTierObject(raw json.RawMessage, keys []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("must be a JSON object")
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
		if _, ok := fields[key]; !ok {
			return nil, fmt.Errorf("missing required property %q", key)
		}
	}
	unknown := make([]string, 0)
	for key := range fields {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unexpected properties %q", unknown)
	}
	return fields, nil
}

func codeReviewRiskTierString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func codeReviewRiskTierInteger(raw json.RawMessage) (int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return 0, errors.New("must be a number")
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err != nil {
		return 0, errors.New("must be a number")
	}
	if value, err := number.Int64(); err == nil {
		return value, nil
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.Trunc(value) != value || value >= math.MaxInt64 || value < math.MinInt64 {
		return 0, errors.New("must be an integer")
	}
	return int64(value), nil
}

type CodeReviewCheckRun struct {
	ID         int64
	Name       string
	HeadSHA    string
	Status     string
	Conclusion string
	AppID      int64
	AppSlug    string
	OutputText string
}

type CodeReviewRiskTierSource string

const (
	CodeReviewRiskTierSourceAuthoritative CodeReviewRiskTierSource = "authoritative"
	CodeReviewRiskTierSourceMissing       CodeReviewRiskTierSource = "missing"
	CodeReviewRiskTierSourceUntrustedApp  CodeReviewRiskTierSource = "untrusted_app"
	CodeReviewRiskTierSourceStaleHead     CodeReviewRiskTierSource = "stale_head"
	CodeReviewRiskTierSourceUnparsable    CodeReviewRiskTierSource = "unparsable"
	CodeReviewRiskTierSourceShadowMode    CodeReviewRiskTierSource = "shadow_mode"
	CodeReviewRiskTierSourceLookupFailed  CodeReviewRiskTierSource = "lookup_failed"
)

var codeReviewRiskTierSourcePrecedence = []CodeReviewRiskTierSource{
	CodeReviewRiskTierSourceLookupFailed,
	CodeReviewRiskTierSourceUnparsable,
	CodeReviewRiskTierSourceStaleHead,
	CodeReviewRiskTierSourceShadowMode,
	CodeReviewRiskTierSourceUntrustedApp,
	CodeReviewRiskTierSourceMissing,
}

func (s CodeReviewRiskTierSource) Validate() error {
	if s == CodeReviewRiskTierSourceAuthoritative {
		return nil
	}
	for _, source := range codeReviewRiskTierSourcePrecedence {
		if s == source {
			return nil
		}
	}
	return fmt.Errorf("invalid CodeReviewRiskTierSource: %q", s)
}

var (
	codeReviewRiskTierRestrictiveness = map[CodeReviewRiskTier]int{
		CodeReviewRiskTierLow: 1, CodeReviewRiskTierMedium: 2, CodeReviewRiskTierUnscored: 3, CodeReviewRiskTierUnavailable: 4, CodeReviewRiskTierHigh: 5,
	}
	codeReviewUIRenderChangeRestrictiveness = map[CodeReviewUIRenderChange]int{
		CodeReviewUIRenderChangeFalse: 1, CodeReviewUIRenderChangeUnknown: 2, CodeReviewUIRenderChangeTrue: 3,
	}
)

type CodeReviewRiskTierObservation struct {
	CheckRunID     int64                    `json:"check_run_id,omitempty"`
	Source         CodeReviewRiskTierSource `json:"source"`
	Tier           CodeReviewRiskTier       `json:"tier,omitempty"`
	UIRenderChange CodeReviewUIRenderChange `json:"ui_render_change,omitempty"`
	HeadSHA        string                   `json:"head_sha,omitempty"`
	IndexID        *string                  `json:"index_id,omitempty"`
	PolicyHash     string                   `json:"policy_hash,omitempty"`
	Mode           CodeReviewRiskTierMode   `json:"mode,omitempty"`
	PublishedAt    *time.Time               `json:"published_at,omitempty"`
	Live           bool                     `json:"live,omitempty"`
}

type CodeReviewRiskTierSignal struct {
	Enabled        bool                            `json:"enabled"`
	HeadSHA        string                          `json:"head_sha,omitempty"`
	AppID          int64                           `json:"app_id,omitempty"`
	AppSlug        string                          `json:"app_slug,omitempty"`
	CheckName      string                          `json:"check_name,omitempty"`
	Source         CodeReviewRiskTierSource        `json:"source,omitempty"`
	Tier           CodeReviewRiskTier              `json:"tier,omitempty"`
	UIRenderChange CodeReviewUIRenderChange        `json:"ui_render_change,omitempty"`
	Observations   []CodeReviewRiskTierObservation `json:"observations,omitempty"`
}

func ResolveCodeReviewRiskTierSignal(policy *CodeReviewRiskTierCheckPolicy, headSHA string, runs []CodeReviewCheckRun) CodeReviewRiskTierSignal {
	if !policy.Active() {
		return CodeReviewRiskTierSignal{}
	}
	signal := newCodeReviewRiskTierSignal(policy, headSHA)
	named := false
	var observations []CodeReviewRiskTierObservation
	for _, run := range runs {
		if run.Name != signal.CheckName {
			continue
		}
		named = true
		if run.AppID == signal.AppID {
			observations = append(observations, observeCodeReviewRiskTierRun(run, signal.HeadSHA))
		}
	}
	if len(observations) == 0 {
		source := CodeReviewRiskTierSourceMissing
		if named {
			source = CodeReviewRiskTierSourceUntrustedApp
		}
		observations = []CodeReviewRiskTierObservation{{Source: source}}
	}
	sort.SliceStable(observations, func(i, j int) bool { return observations[i].CheckRunID < observations[j].CheckRunID })
	return signal.withObservations(observations)
}

func CodeReviewRiskTierLookupFailedSignal(policy *CodeReviewRiskTierCheckPolicy, headSHA string) CodeReviewRiskTierSignal {
	if !policy.Active() {
		return CodeReviewRiskTierSignal{}
	}
	return newCodeReviewRiskTierSignal(policy, headSHA).withObservations([]CodeReviewRiskTierObservation{{Source: CodeReviewRiskTierSourceLookupFailed}})
}

func newCodeReviewRiskTierSignal(policy *CodeReviewRiskTierCheckPolicy, headSHA string) CodeReviewRiskTierSignal {
	return CodeReviewRiskTierSignal{Enabled: true, HeadSHA: strings.ToLower(strings.TrimSpace(headSHA)), AppID: policy.AppID, AppSlug: policy.AppSlug, CheckName: policy.EffectiveCheckName()}
}

func observeCodeReviewRiskTierRun(run CodeReviewCheckRun, head string) CodeReviewRiskTierObservation {
	observation := CodeReviewRiskTierObservation{CheckRunID: run.ID, Source: CodeReviewRiskTierSourceStaleHead}
	if !strings.EqualFold(strings.TrimSpace(run.HeadSHA), head) {
		return observation
	}
	payload, err := ExtractCodeReviewRiskTierPayload(run.OutputText)
	if err != nil {
		observation.Source = CodeReviewRiskTierSourceUnparsable
		return observation
	}
	publishedAt := payload.PublishedAt
	observation.Tier, observation.UIRenderChange = payload.Tier, payload.UIRenderChange
	observation.HeadSHA, observation.IndexID, observation.PolicyHash = payload.Generation.HeadSHA, payload.Generation.IndexID, payload.Generation.PolicyHash
	observation.Mode, observation.PublishedAt = payload.Generation.Mode, &publishedAt
	switch {
	case payload.Generation.HeadSHA != head:
		observation.Source = CodeReviewRiskTierSourceStaleHead
	case payload.Generation.Mode != CodeReviewRiskTierModeEnforce:
		observation.Source = CodeReviewRiskTierSourceShadowMode
	default:
		observation.Source = CodeReviewRiskTierSourceAuthoritative
	}
	return observation
}

func (s CodeReviewRiskTierSignal) withObservations(observations []CodeReviewRiskTierObservation) CodeReviewRiskTierSignal {
	s.Observations = append([]CodeReviewRiskTierObservation(nil), observations...)
	s.Source, s.Tier, s.UIRenderChange = CodeReviewRiskTierSourceAuthoritative, "", ""
	unauthoritative := map[CodeReviewRiskTierSource]bool{}
	for _, observation := range s.Observations {
		if observation.Source != CodeReviewRiskTierSourceAuthoritative {
			unauthoritative[observation.Source] = true
			continue
		}
		if codeReviewRiskTierRestrictiveness[observation.Tier] > codeReviewRiskTierRestrictiveness[s.Tier] {
			s.Tier = observation.Tier
		}
		if codeReviewUIRenderChangeRestrictiveness[observation.UIRenderChange] > codeReviewUIRenderChangeRestrictiveness[s.UIRenderChange] {
			s.UIRenderChange = observation.UIRenderChange
		}
	}
	if len(s.Observations) == 0 {
		unauthoritative[CodeReviewRiskTierSourceMissing] = true
	}
	for _, source := range codeReviewRiskTierSourcePrecedence {
		if unauthoritative[source] {
			s.Source = source
			break
		}
	}
	return s
}

func (s CodeReviewRiskTierSignal) WithLive(live CodeReviewRiskTierSignal) CodeReviewRiskTierSignal {
	if !s.Enabled || !live.Enabled || (live.Source == s.Source && live.Tier == s.Tier && live.UIRenderChange == s.UIRenderChange) {
		return s
	}
	observations := append([]CodeReviewRiskTierObservation(nil), s.Observations...)
	for _, observation := range live.Observations {
		observation.Live = true
		observations = append(observations, observation)
	}
	return s.withObservations(observations)
}

func (s *CodeReviewRiskTierSignal) Active() bool {
	return s != nil && s.Enabled
}

func (s *CodeReviewRiskTierSignal) Authoritative() bool {
	return s.Active() && s.Source == CodeReviewRiskTierSourceAuthoritative
}

func (s *CodeReviewRiskTierSignal) AuthoritativeUIRenderChange() CodeReviewUIRenderChange {
	switch {
	case s.Active() && s.UIRenderChange == CodeReviewUIRenderChangeTrue:
		return CodeReviewUIRenderChangeTrue
	case s.Authoritative():
		return s.UIRenderChange
	default:
		return ""
	}
}

func (s *CodeReviewRiskTierSignal) WaivesSizeSafeguard() bool {
	return s.Authoritative() && (s.Tier == CodeReviewRiskTierLow || s.Tier == CodeReviewRiskTierMedium)
}

func (s *CodeReviewRiskTierSignal) decidingObservation() *CodeReviewRiskTierObservation {
	var deciding *CodeReviewRiskTierObservation
	for i := range s.Observations {
		observation := &s.Observations[i]
		if s.Source == CodeReviewRiskTierSourceAuthoritative {
			if observation.Source == CodeReviewRiskTierSourceAuthoritative && observation.Tier == s.Tier {
				deciding = observation
			}
			continue
		}
		if deciding == nil && observation.Source == s.Source {
			deciding = observation
		}
	}
	return deciding
}

type CodeReviewSizeSafeguardOutcome string

const (
	CodeReviewSizeSafeguardApplied CodeReviewSizeSafeguardOutcome = "applied"
	CodeReviewSizeSafeguardWaived  CodeReviewSizeSafeguardOutcome = "waived"
)

type CodeReviewSizeMeasurement struct {
	Actual   int  `json:"actual"`
	Limit    int  `json:"limit"`
	Exceeded bool `json:"exceeded"`
}

type CodeReviewSizeSafeguardDecision struct {
	Outcome      CodeReviewSizeSafeguardOutcome  `json:"outcome"`
	Source       CodeReviewRiskTierSource        `json:"source"`
	Tier         CodeReviewRiskTier              `json:"tier,omitempty"`
	HeadSHA      string                          `json:"head_sha"`
	AppID        int64                           `json:"app_id,omitempty"`
	CheckRunID   int64                           `json:"check_run_id,omitempty"`
	Mode         CodeReviewRiskTierMode          `json:"mode,omitempty"`
	IndexID      *string                         `json:"index_id,omitempty"`
	PolicyHash   string                          `json:"policy_hash,omitempty"`
	PublishedAt  *time.Time                      `json:"published_at,omitempty"`
	Files        CodeReviewSizeMeasurement       `json:"files"`
	Additions    CodeReviewSizeMeasurement       `json:"additions"`
	Deletions    CodeReviewSizeMeasurement       `json:"deletions"`
	Observations []CodeReviewRiskTierObservation `json:"observations,omitempty"`
}

func (s *CodeReviewRiskTierSignal) SizeSafeguardDecision(files, additions, deletions CodeReviewSizeMeasurement) CodeReviewSizeSafeguardDecision {
	decision := CodeReviewSizeSafeguardDecision{
		Outcome: CodeReviewSizeSafeguardApplied, Source: s.Source, Tier: s.Tier, HeadSHA: s.HeadSHA, AppID: s.AppID,
		Files: files, Additions: additions, Deletions: deletions,
		Observations: append([]CodeReviewRiskTierObservation(nil), s.Observations...),
	}
	if s.WaivesSizeSafeguard() {
		decision.Outcome = CodeReviewSizeSafeguardWaived
	}
	if deciding := s.decidingObservation(); deciding != nil {
		decision.CheckRunID, decision.Mode, decision.IndexID, decision.PolicyHash, decision.PublishedAt = deciding.CheckRunID, deciding.Mode, deciding.IndexID, deciding.PolicyHash, deciding.PublishedAt
	}
	return decision
}
