package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	legacyJobRouteMaxBytes       = 1 << 20
	legacyJobRouteMaxOccurrences = 128
)

// These flags describe observations, never consumer ownership or permission to
// dispatch. In particular, the absence of a route problem does not validate the
// worker's other typed fields or its stricter recovery-mode requirements.
type legacyJobRouteProblems uint16

const (
	legacyJobRouteMissing legacyJobRouteProblems = 1 << iota
	legacyJobRouteNull
	legacyJobRouteEmpty
	legacyJobRouteWrongType
	legacyJobRouteInvalidUUID
	legacyJobRouteZeroUUID
	legacyJobRouteDuplicate
	legacyJobRouteAlias
	legacyJobRouteMalformedJSON
	legacyJobRouteNonObject
	legacyJobRouteTrailingJSON
	legacyJobRouteByteOverflow
	legacyJobRouteAnchorOverflow
)

func (p legacyJobRouteProblems) validate() error {
	const known = legacyJobRouteMissing | legacyJobRouteNull | legacyJobRouteEmpty |
		legacyJobRouteWrongType | legacyJobRouteInvalidUUID | legacyJobRouteZeroUUID |
		legacyJobRouteDuplicate | legacyJobRouteAlias | legacyJobRouteMalformedJSON |
		legacyJobRouteNonObject | legacyJobRouteTrailingJSON | legacyJobRouteByteOverflow |
		legacyJobRouteAnchorOverflow
	if p & ^known != 0 {
		return errors.New("unknown legacy job route problem")
	}
	return nil
}

type legacyJobRouteField struct {
	anchors  []uuid.UUID
	problems legacyJobRouteProblems
}

type legacyJobRouteObservation struct {
	jobOrgID uuid.UUID
	org      legacyJobRouteField
	session  legacyJobRouteField
	thread   legacyJobRouteField
	problems legacyJobRouteProblems

	// A payload org is only a candidate anchor. It cannot authorize reading a
	// different tenant, including when the legacy worker would honor it.
	orgMismatch bool
	orgUsesJob  bool

	// Positive evidence that the route fields alone fail legacy string/UUID
	// decoding. False is not evidence that the entire job can dispatch. Zero
	// UUIDs are recorded separately: uuid.Parse accepts them.
	routeDecodingFails bool

	// A valid session cannot safely be narrowed to a payload lane when the
	// legacy string value is absent/empty or fails UUID parsing. Ordinary
	// workers fall back, while recovery modes can reject the same payload.
	// This describes final strings, not every retained duplicate anchor;
	// false cannot narrow ambiguous or malformed evidence to a lane.
	sessionWideFallback bool
}

func (o legacyJobRouteObservation) candidateOrgIDs() []uuid.UUID {
	var ids []uuid.UUID
	if o.jobOrgID != uuid.Nil {
		ids = append(ids, o.jobOrgID)
	}
	for _, id := range o.org.anchors {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (o legacyJobRouteObservation) sessionIDs() []uuid.UUID {
	return slices.Clone(o.session.anchors)
}

func (o legacyJobRouteObservation) threadIDs() []uuid.UUID {
	return slices.Clone(o.thread.anchors)
}

func (o legacyJobRouteObservation) incomplete() bool {
	return o.problems&(legacyJobRouteByteOverflow|legacyJobRouteAnchorOverflow) != 0
}

// decodeLegacyJobRoute preserves route evidence from legacy run_agent and
// continue_session jobs. It deliberately does not use the strict versioned
// binding decoder: duplicates and folded aliases must not erase scoped anchors.
// Callers must reconcile ambiguous/incomplete observations against structured
// references within their already authorized tenant; this function does no DB
// lookups and cannot establish consumer ownership or tenant membership.
func decodeLegacyJobRoute(jobOrgID uuid.UUID, payload json.RawMessage) legacyJobRouteObservation {
	empty := legacyJobRouteObservation{jobOrgID: jobOrgID}
	if len(payload) > legacyJobRouteMaxBytes {
		empty.problems = legacyJobRouteByteOverflow
		return empty
	}
	// Validate the complete document first. A valid prefix of malformed or
	// trailing JSON must not fabricate a route the worker cannot decode.
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var document json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		empty.problems = legacyJobRouteMalformedJSON
		return empty
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		empty.problems = legacyJobRouteTrailingJSON
		return empty
	}
	if document = bytes.TrimSpace(document); len(document) == 0 || document[0] != '{' {
		empty.problems = legacyJobRouteNonObject
		return empty
	}

	observation := legacyJobRouteObservation{
		jobOrgID: jobOrgID,
		org:      legacyJobRouteField{problems: legacyJobRouteMissing},
		session:  legacyJobRouteField{problems: legacyJobRouteMissing},
		thread:   legacyJobRouteField{problems: legacyJobRouteMissing},
	}
	// Mutation is local to construction. Only defensive anchor copies are
	// returned by accessors; no raw payload or caller-owned buffers are retained.
	fields := [...]*legacyJobRouteField{&observation.org, &observation.session, &observation.thread}
	keys := [...]string{"org_id", "session_id", "thread_id"}
	var values [3]string
	var occurrences [3]int
	total := 0
	decoder = json.NewDecoder(bytes.NewReader(document))
	if _, err := decoder.Token(); err != nil {
		empty.problems = legacyJobRouteMalformedJSON
		return empty
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			empty.problems = legacyJobRouteMalformedJSON
			return empty
		}
		key, ok := token.(string)
		if !ok {
			empty.problems = legacyJobRouteMalformedJSON
			return empty
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			empty.problems = legacyJobRouteMalformedJSON
			return empty
		}
		for i, canonical := range keys {
			if !strings.EqualFold(key, canonical) {
				continue
			}
			// Bound occurrences rather than unique IDs: repeated identical keys
			// cannot evade the ambiguity/discovery budget through deduplication.
			total++
			if total > legacyJobRouteMaxOccurrences {
				observation.problems |= legacyJobRouteAnchorOverflow
				return observation
			}
			field := fields[i]
			field.problems &^= legacyJobRouteMissing
			occurrences[i]++
			if occurrences[i] > 1 {
				field.problems |= legacyJobRouteDuplicate
			}
			if key != canonical {
				field.problems |= legacyJobRouteAlias
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				field.problems |= legacyJobRouteNull
				// encoding/json leaves an earlier string unchanged on null.
				break
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				field.problems |= legacyJobRouteWrongType
				observation.routeDecodingFails = true
				break
			}
			values[i] = value
			if value == "" {
				field.problems |= legacyJobRouteEmpty
				break
			}
			id, err := uuid.Parse(value)
			if err != nil {
				field.problems |= legacyJobRouteInvalidUUID
				break
			}
			if id == uuid.Nil {
				field.problems |= legacyJobRouteZeroUUID
				break
			}
			if !slices.Contains(field.anchors, id) {
				field.anchors = append(field.anchors, id)
			}
			if i == 0 && id != jobOrgID {
				observation.orgMismatch = true
			}
			break
		}
	}
	if _, err := decoder.Token(); err != nil {
		empty.problems = legacyJobRouteMalformedJSON
		return empty
	}
	observation.orgUsesJob = values[0] == ""
	if values[0] != "" {
		if _, err := uuid.Parse(values[0]); err != nil {
			observation.routeDecodingFails = true
		}
	}
	sessionID, sessionErr := uuid.Parse(values[1])
	if sessionErr != nil {
		observation.routeDecodingFails = true
	}
	_, threadErr := uuid.Parse(values[2])
	observation.sessionWideFallback = sessionErr == nil && sessionID != uuid.Nil && threadErr != nil
	return observation
}
