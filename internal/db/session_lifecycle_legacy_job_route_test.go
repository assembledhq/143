package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLegacyJobRouteFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		raw      string
		missing  bool
		problems legacyJobRouteProblems
	}{
		{name: "missing", missing: true, problems: legacyJobRouteMissing},
		{name: "null", raw: `null`, problems: legacyJobRouteNull},
		{name: "empty", raw: `""`, problems: legacyJobRouteEmpty},
		{name: "invalid UUID", raw: `"not-a-uuid"`, problems: legacyJobRouteInvalidUUID},
		{name: "zero UUID", raw: `"00000000-0000-0000-0000-000000000000"`, problems: legacyJobRouteZeroUUID},
		{name: "number", raw: `42`, problems: legacyJobRouteWrongType},
		{name: "boolean", raw: `true`, problems: legacyJobRouteWrongType},
		{name: "object", raw: `{}`, problems: legacyJobRouteWrongType},
		{name: "array", raw: `[]`, problems: legacyJobRouteWrongType},
	}
	for _, route := range []struct {
		name  string
		index int
	}{{name: "org_id", index: 0}, {name: "session_id", index: 1}, {name: "thread_id", index: 2}} {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					orgID, sessionID, threadID := legacyJobRouteTestIDs()
					fields := []string{
						fmt.Sprintf(`"org_id":%q`, orgID),
						fmt.Sprintf(`"session_id":%q`, sessionID),
						fmt.Sprintf(`"thread_id":%q`, threadID),
					}
					if tt.missing {
						fields = append(fields[:route.index], fields[route.index+1:]...)
					} else {
						fields[route.index] = fmt.Sprintf(`%q:%s`, route.name, tt.raw)
					}
					payload := json.RawMessage("{" + strings.Join(fields, ",") + "}")
					expected := legacyJobRouteTestObservation()
					replaced := legacyJobRouteField{problems: tt.problems}
					switch route.index {
					case 0:
						expected.org = replaced
						expected.orgUsesJob = tt.problems != legacyJobRouteInvalidUUID && tt.problems != legacyJobRouteZeroUUID
						expected.routeDecodingFails = tt.problems == legacyJobRouteInvalidUUID || tt.problems == legacyJobRouteWrongType
					case 1:
						expected.session = replaced
						expected.routeDecodingFails = tt.problems != legacyJobRouteZeroUUID
					case 2:
						expected.thread = replaced
						expected.sessionWideFallback = tt.problems != legacyJobRouteZeroUUID
						expected.routeDecodingFails = tt.problems == legacyJobRouteWrongType
					}
					actual := decodeLegacyJobRoute(orgID, payload)
					require.Equal(t, expected, actual, "a malformed route field must preserve every independent valid anchor and its exact fallback evidence")
				})
			}
		})
	}
}

func TestLegacyJobRouteAliasesAndUUIDSpellings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		orgKey  string
		sessKey string
		laneKey string
		format  func(uuid.UUID) string
		alias   bool
	}{
		{name: "canonical", orgKey: "org_id", sessKey: "session_id", laneKey: "thread_id", format: func(id uuid.UUID) string { return id.String() }},
		{name: "raw hex", orgKey: "org_id", sessKey: "session_id", laneKey: "thread_id", format: func(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "") }},
		{name: "URN", orgKey: "org_id", sessKey: "session_id", laneKey: "thread_id", format: func(id uuid.UUID) string { return id.URN() }},
		{name: "braced", orgKey: "org_id", sessKey: "session_id", laneKey: "thread_id", format: func(id uuid.UUID) string { return "{" + id.String() + "}" }},
		{name: "ASCII folded", orgKey: "ORG_ID", sessKey: "Session_ID", laneKey: "THREAD_ID", format: func(id uuid.UUID) string { return id.String() }, alias: true},
		{name: "Unicode folded", orgKey: "ORG_ID", sessKey: "ſeſſion_id", laneKey: "THREAD_ID", format: func(id uuid.UUID) string { return id.String() }, alias: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := legacyJobRouteTestIDs()
			payload := json.RawMessage(fmt.Sprintf(`{%q:%q,%q:%q,%q:%q}`, tt.orgKey, tt.format(orgID), tt.sessKey, tt.format(sessionID), tt.laneKey, tt.format(threadID)))
			expected := legacyJobRouteTestObservation()
			if tt.alias {
				expected.org.problems = legacyJobRouteAlias
				expected.session.problems = legacyJobRouteAlias
				expected.thread.problems = legacyJobRouteAlias
			}
			require.Equal(t, expected, decodeLegacyJobRoute(orgID, payload), "route observation must accept every worker UUID spelling and EqualFold key while exposing aliases")
		})
	}
}

func TestLegacyJobRouteDuplicates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		key      string
		value    string
		problems legacyJobRouteProblems
		fails    bool
		extraID  bool
	}{
		{name: "exact same anchor", key: `session_id`, value: `"22222222-2222-4222-8222-222222222222"`, problems: legacyJobRouteDuplicate},
		{name: "escaped key", key: `se\u0073sion_id`, value: `"22222222-2222-4222-8222-222222222222"`, problems: legacyJobRouteDuplicate},
		{name: "folded alias", key: `SESSION_ID`, value: `"55555555-5555-4555-8555-555555555555"`, problems: legacyJobRouteDuplicate | legacyJobRouteAlias, extraID: true},
		{name: "Unicode alias", key: `ſeſſion_id`, value: `"55555555-5555-4555-8555-555555555555"`, problems: legacyJobRouteDuplicate | legacyJobRouteAlias, extraID: true},
		{name: "null retains prior string", key: `session_id`, value: `null`, problems: legacyJobRouteDuplicate | legacyJobRouteNull},
		{name: "invalid retains prior anchor", key: `session_id`, value: `"broken"`, problems: legacyJobRouteDuplicate | legacyJobRouteInvalidUUID, fails: true},
		{name: "wrong type retains prior anchor", key: `session_id`, value: `[]`, problems: legacyJobRouteDuplicate | legacyJobRouteWrongType, fails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := legacyJobRouteTestIDs()
			payload := json.RawMessage(fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q,"%s":%s}`, orgID, sessionID, threadID, tt.key, tt.value))
			expected := legacyJobRouteTestObservation()
			expected.session.problems = tt.problems
			expected.routeDecodingFails = tt.fails
			if tt.extraID {
				expected.session.anchors = append(expected.session.anchors, uuid.MustParse("55555555-5555-4555-8555-555555555555"))
			}
			require.Equal(t, expected, decodeLegacyJobRoute(orgID, payload), "duplicate and aliased occurrences must retain all anchors without last-key-wins clearance")
		})
	}
}

func TestLegacyJobRouteRepeatedStringSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		suffix    string
		orgFlags  legacyJobRouteProblems
		laneFlags legacyJobRouteProblems
		usesJob   bool
		fallback  bool
		fails     bool
	}{
		{name: "null preserves explicit org", suffix: `,"org_id":null`, orgFlags: legacyJobRouteDuplicate | legacyJobRouteNull},
		{name: "empty org restores context fallback", suffix: `,"org_id":""`, orgFlags: legacyJobRouteDuplicate | legacyJobRouteEmpty, usesJob: true},
		{name: "null preserves empty org fallback", suffix: `,"org_id":"","org_id":null`, orgFlags: legacyJobRouteDuplicate | legacyJobRouteEmpty | legacyJobRouteNull, usesJob: true},
		{name: "null preserves invalid org string", suffix: `,"org_id":"broken","org_id":null`, orgFlags: legacyJobRouteDuplicate | legacyJobRouteInvalidUUID | legacyJobRouteNull, fails: true},
		{name: "later valid string does not erase type error", suffix: `,"org_id":7,"org_id":"11111111-1111-4111-8111-111111111111"`, orgFlags: legacyJobRouteDuplicate | legacyJobRouteWrongType, fails: true},
		{name: "null preserves explicit lane", suffix: `,"thread_id":null`, laneFlags: legacyJobRouteDuplicate | legacyJobRouteNull},
		{name: "null preserves invalid lane fallback", suffix: `,"thread_id":"broken","thread_id":null`, laneFlags: legacyJobRouteDuplicate | legacyJobRouteInvalidUUID | legacyJobRouteNull, fallback: true},
		{name: "empty lane enables fallback", suffix: `,"thread_id":""`, laneFlags: legacyJobRouteDuplicate | legacyJobRouteEmpty, fallback: true},
		{name: "null preserves empty lane fallback", suffix: `,"thread_id":"","thread_id":null`, laneFlags: legacyJobRouteDuplicate | legacyJobRouteEmpty | legacyJobRouteNull, fallback: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := legacyJobRouteTestIDs()
			payload := json.RawMessage(fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q%s}`, orgID, sessionID, threadID, tt.suffix))
			expected := legacyJobRouteTestObservation()
			expected.org.problems, expected.thread.problems = tt.orgFlags, tt.laneFlags
			expected.orgUsesJob, expected.sessionWideFallback, expected.routeDecodingFails = tt.usesJob, tt.fallback, tt.fails
			require.Equal(t, expected, decodeLegacyJobRoute(orgID, payload), "observations must preserve prior anchors and match legacy null, string, and type-error behavior independently")
		})
	}
}

func TestLegacyJobRouteScopeEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		payload  string
		expected legacyJobRouteObservation
	}{
		{
			name:    "foreign explicit org is only an anchor",
			payload: `{"org_id":"99999999-9999-4999-8999-999999999999","session_id":"22222222-2222-4222-8222-222222222222","thread_id":"33333333-3333-4333-8333-333333333333"}`,
			expected: legacyJobRouteObservation{
				jobOrgID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
				org:      legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("99999999-9999-4999-8999-999999999999")}},
				session:  legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("22222222-2222-4222-8222-222222222222")}},
				thread:   legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("33333333-3333-4333-8333-333333333333")}}, orgMismatch: true,
			},
		},
		{
			name:    "contradictory session and thread are independent anchors",
			payload: `{"session_id":"55555555-5555-4555-8555-555555555555","thread_id":"33333333-3333-4333-8333-333333333333"}`,
			expected: legacyJobRouteObservation{
				jobOrgID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
				org:      legacyJobRouteField{problems: legacyJobRouteMissing},
				session:  legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("55555555-5555-4555-8555-555555555555")}},
				thread:   legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("33333333-3333-4333-8333-333333333333")}}, orgUsesJob: true,
			},
		},
		{
			name:    "malformed final session cannot narrow retained anchor",
			payload: `{"session_id":"22222222-2222-4222-8222-222222222222","session_id":"broken"}`,
			expected: legacyJobRouteObservation{
				jobOrgID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
				org:      legacyJobRouteField{problems: legacyJobRouteMissing},
				session:  legacyJobRouteField{anchors: []uuid.UUID{uuid.MustParse("22222222-2222-4222-8222-222222222222")}, problems: legacyJobRouteDuplicate | legacyJobRouteInvalidUUID},
				thread:   legacyJobRouteField{problems: legacyJobRouteMissing}, orgUsesJob: true, routeDecodingFails: true,
			},
		},
		{
			name:    "nested routes are not top level anchors",
			payload: `{"domain":{"org_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}}`,
			expected: legacyJobRouteObservation{
				jobOrgID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
				org:      legacyJobRouteField{problems: legacyJobRouteMissing}, session: legacyJobRouteField{problems: legacyJobRouteMissing},
				thread: legacyJobRouteField{problems: legacyJobRouteMissing}, orgUsesJob: true, routeDecodingFails: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, _, _ := legacyJobRouteTestIDs()
			require.Equal(t, tt.expected, decodeLegacyJobRoute(orgID, json.RawMessage(tt.payload)), "the decoder must expose anchors without resolving tenant or thread membership")
		})
	}
}

func TestLegacyJobRoutePayloadShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		payload  string
		problems legacyJobRouteProblems
	}{
		{name: "empty", problems: legacyJobRouteMalformedJSON},
		{name: "broken syntax", payload: `{"session_id":`, problems: legacyJobRouteMalformedJSON},
		{name: "valid anchor before broken syntax", payload: `{"session_id":"22222222-2222-4222-8222-222222222222",`, problems: legacyJobRouteMalformedJSON},
		{name: "null", payload: `null`, problems: legacyJobRouteNonObject},
		{name: "array", payload: `[]`, problems: legacyJobRouteNonObject},
		{name: "string", payload: `"value"`, problems: legacyJobRouteNonObject},
		{name: "number", payload: `1`, problems: legacyJobRouteNonObject},
		{name: "boolean", payload: `false`, problems: legacyJobRouteNonObject},
		{name: "trailing object", payload: `{"session_id":"22222222-2222-4222-8222-222222222222"}{}`, problems: legacyJobRouteTrailingJSON},
		{name: "trailing garbage", payload: `{"session_id":"22222222-2222-4222-8222-222222222222"}bad`, problems: legacyJobRouteTrailingJSON},
		{name: "byte overflow", payload: strings.Repeat(" ", legacyJobRouteMaxBytes+1), problems: legacyJobRouteByteOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, _, _ := legacyJobRouteTestIDs()
			expected := legacyJobRouteObservation{jobOrgID: orgID, problems: tt.problems}
			require.Equal(t, expected, decodeLegacyJobRoute(orgID, json.RawMessage(tt.payload)), "undecodable or oversized documents must never return fabricated payload routes")
		})
	}
}

func TestLegacyJobRouteByteLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		extra    int
		overflow bool
	}{
		{name: "exact byte limit"},
		{name: "one byte past limit", extra: 1, overflow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, threadID := legacyJobRouteTestIDs()
			object := fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q}`, orgID, sessionID, threadID)
			payload := json.RawMessage(object + strings.Repeat(" ", legacyJobRouteMaxBytes-len(object)+tt.extra))
			expected := legacyJobRouteTestObservation()
			if tt.overflow {
				expected = legacyJobRouteObservation{jobOrgID: orgID, problems: legacyJobRouteByteOverflow}
			}
			actual := decodeLegacyJobRoute(orgID, payload)
			require.Equal(t, expected, actual, "the byte budget must accept its boundary and expose no routes from oversized input")
			require.Equal(t, tt.overflow, actual.incomplete(), "byte overflow must be explicit incomplete discovery rather than a clear scope")
		})
	}
}

func TestLegacyJobRouteAnchorOverflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		unique bool
	}{
		{name: "distinct valid anchors", unique: true},
		{name: "duplicates still spend occurrence budget"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := legacyJobRouteTestIDs()
			var fields []string
			var expectedIDs []uuid.UUID
			for i := range legacyJobRouteMaxOccurrences + 1 {
				id := sessionID
				if tt.unique {
					id = uuid.UUID{15: byte(i + 1)}
				}
				fields = append(fields, fmt.Sprintf(`"session_id":%q`, id))
				if i < legacyJobRouteMaxOccurrences && (tt.unique || i == 0) {
					expectedIDs = append(expectedIDs, id)
				}
			}
			payload := json.RawMessage("{" + strings.Join(fields, ",") + "}")
			expected := legacyJobRouteObservation{
				jobOrgID: orgID, org: legacyJobRouteField{problems: legacyJobRouteMissing},
				session: legacyJobRouteField{anchors: expectedIDs, problems: legacyJobRouteDuplicate},
				thread:  legacyJobRouteField{problems: legacyJobRouteMissing}, problems: legacyJobRouteAnchorOverflow,
			}
			actual := decodeLegacyJobRoute(orgID, payload)
			require.Equal(t, expected, actual, "overflow must retain bounded observed anchors and explicitly refuse complete discovery")
			require.True(t, actual.incomplete(), "an occurrence overflow must never be interpreted as a complete scope search")
		})
	}
}

func TestLegacyJobRouteDefensiveCopies(t *testing.T) {
	t.Parallel()
	orgID, sessionID, threadID := legacyJobRouteTestIDs()
	foreignOrg := uuid.MustParse("99999999-9999-4999-8999-999999999999")
	payload := json.RawMessage(fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q}`, foreignOrg, sessionID, threadID))
	actual := decodeLegacyJobRoute(orgID, payload)
	expected := legacyJobRouteTestObservation()
	expected.org.anchors = []uuid.UUID{foreignOrg}
	expected.orgMismatch = true
	for i := range payload {
		payload[i] = ' '
	}
	orgs := actual.candidateOrgIDs()
	sessions := actual.sessionIDs()
	threads := actual.threadIDs()
	require.Equal(t, []uuid.UUID{orgID, foreignOrg}, orgs, "candidate orgs must preserve both row tenant and foreign payload evidence")
	require.Equal(t, []uuid.UUID{sessionID}, sessions, "session anchors must survive caller payload mutation")
	require.Equal(t, []uuid.UUID{threadID}, threads, "thread anchors must survive caller payload mutation")
	orgs[0], orgs[1], sessions[0], threads[0] = uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil
	require.Equal(t, expected, actual, "mutating returned slices or input bytes must not change the retained observation")
	require.Equal(t, []uuid.UUID{orgID, foreignOrg}, actual.candidateOrgIDs(), "candidate org access must return a fresh defensive slice each time")
}

func TestLegacyJobRouteProblemValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		problems legacyJobRouteProblems
		invalid  bool
	}{
		{name: "no problems"},
		{name: "combined finite problems", problems: legacyJobRouteMissing | legacyJobRouteNull | legacyJobRouteEmpty | legacyJobRouteWrongType | legacyJobRouteInvalidUUID | legacyJobRouteZeroUUID | legacyJobRouteDuplicate | legacyJobRouteAlias | legacyJobRouteMalformedJSON | legacyJobRouteNonObject | legacyJobRouteTrailingJSON | legacyJobRouteByteOverflow | legacyJobRouteAnchorOverflow},
		{name: "unknown flag", problems: 1 << 15, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.problems.validate()
			if tt.invalid {
				require.EqualError(t, err, "unknown legacy job route problem", "finite route-problem flags must reject unknown observations")
				return
			}
			require.NoError(t, err, "all known route problems must compose without introducing untyped outcomes")
		})
	}
}

func legacyJobRouteTestIDs() (uuid.UUID, uuid.UUID, uuid.UUID) {
	return uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		uuid.MustParse("33333333-3333-4333-8333-333333333333")
}

func legacyJobRouteTestObservation() legacyJobRouteObservation {
	orgID, sessionID, threadID := legacyJobRouteTestIDs()
	return legacyJobRouteObservation{
		jobOrgID: orgID,
		org:      legacyJobRouteField{anchors: []uuid.UUID{orgID}},
		session:  legacyJobRouteField{anchors: []uuid.UUID{sessionID}},
		thread:   legacyJobRouteField{anchors: []uuid.UUID{threadID}},
	}
}
