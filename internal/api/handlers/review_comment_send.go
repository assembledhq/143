package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// maxReviewCommentResolveIDsPerMessage caps how many review comments a single
// follow-up message may resolve atomically. Used by both session-level
// SendMessage and thread-level SendThreadMessage so the resolve-with-message
// surface stays uniform across send paths.
const maxReviewCommentResolveIDsPerMessage = 50

// parseAndDedupeReviewCommentIDs validates and deduplicates the wire-level
// resolve_review_comment_ids body field. The cap is enforced first so a
// runaway client can't make us parse a million UUIDs before we reject. Order
// is preserved on first occurrence so the error message and downstream
// audits emit IDs in a deterministic order.
//
// Returns a parseReviewCommentIDsError carrying the HTTP shape of the failure
// when the input is malformed, so handlers can render the response without
// duplicating the error-mapping logic.
func parseAndDedupeReviewCommentIDs(raw []string) ([]uuid.UUID, *parseReviewCommentIDsError) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxReviewCommentResolveIDsPerMessage {
		return nil, &parseReviewCommentIDsError{
			status:  http.StatusBadRequest,
			code:    "TOO_MANY_REVIEW_COMMENT_IDS",
			message: fmt.Sprintf("at most %d review comment ids may be resolved per message", maxReviewCommentResolveIDsPerMessage),
		}
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		parsed, err := uuid.Parse(s)
		if err != nil {
			return nil, &parseReviewCommentIDsError{
				status:  http.StatusBadRequest,
				code:    "INVALID_REVIEW_COMMENT_ID",
				message: "invalid review comment id: " + s,
			}
		}
		if _, dup := seen[parsed]; dup {
			continue
		}
		seen[parsed] = struct{}{}
		out = append(out, parsed)
	}
	return out, nil
}

// parseReviewCommentIDsError is the HTTP shape of a body-parsing failure for
// resolve_review_comment_ids. Pure (no http.ResponseWriter dependency) so the
// helper can stay free of net/http coupling at the parse stage.
type parseReviewCommentIDsError struct {
	status  int
	code    string
	message string
}

func (e *parseReviewCommentIDsError) write(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, e.status, e.code, e.message)
}

// renderReviewCommentResolveError maps the structured errors returned by the
// resolve flow (db sentinel for missing IDs, the in-package "not configured"
// case) to HTTP responses. Both SendMessage and SendThreadMessage use this so
// the wire shape stays identical across the two surfaces.
//
// Returns true iff the error was recognized and an HTTP response was written;
// callers should fall back to a generic 500 otherwise.
func renderReviewCommentResolveError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var notInSession *db.ErrReviewCommentsNotInSession
	if errors.As(err, &notInSession) {
		const maxMissingInError = 5
		ids := notInSession.Missing
		if len(ids) > maxMissingInError {
			ids = ids[:maxMissingInError]
		}
		strs := make([]string, 0, len(ids))
		for _, id := range ids {
			strs = append(strs, id.String())
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REVIEW_COMMENT_IDS",
			"review comment ids do not belong to this session: "+strings.Join(strs, ", "))
		return true
	}
	return false
}

// currentResolutionPass returns the pass number to record on a comment that
// is being resolved during the current request. Matches the semantics used
// by the PATCH /review-comments handler: the user's resolving action belongs
// to the session's current turn (with a fallback to 1 for not-yet-started
// sessions where CurrentTurn is still 0).
func currentResolutionPass(session *models.Session) int {
	if session == nil || session.CurrentTurn == 0 {
		return 1
	}
	return session.CurrentTurn
}

// emitReviewCommentResolutionAudits records one audit row per comment whose
// resolved state actually changed. Mirrors the audit shape emitted by the
// direct PATCH /review-comments handler so audit consumers see consistent
// before/after values regardless of which surface triggered the resolution.
// The resolved_via_message flag lets consumers tell the two surfaces apart.
//
// Lives at the package level (not on a handler receiver) because both
// session-level SendMessage and thread-level SendThreadMessage emit the
// same shape — keeping a single source of truth here avoids audit drift
// between the two surfaces as they evolve.
func emitReviewCommentResolutionAudits(
	emitter *db.AuditEmitter,
	logger zerolog.Logger,
	r *http.Request,
	sessionID uuid.UUID,
	messageID int64,
	resolved []models.SessionReviewComment,
) {
	if len(resolved) == 0 {
		return
	}
	entries := make([]userAuditEntry, 0, len(resolved))
	for _, c := range resolved {
		resID := c.ID.String()
		sid := sessionID
		entries = append(entries, userAuditEntry{
			Action:       models.AuditActionSessionReviewCommentUpdated,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resID,
			SessionID:    &sid,
			Details: marshalAuditDetails(logger, map[string]any{
				"review_comment_id":    c.ID.String(),
				"session_id":           sid.String(),
				"file_path":            c.FilePath,
				"line_number":          c.LineNumber,
				"diff_side":            c.DiffSide,
				"pass_number":          c.PassNumber,
				"body_length":          len(c.Body),
				"resolved_via_message": true,
				"message_id":           messageID,
				"changes": map[string]any{
					"resolved": auditChange(false, true),
				},
			}),
		})
	}
	// One INSERT regardless of N — keeps post-commit latency O(1) in the
	// number of attached comments.
	emitUserAuditsWithSession(emitter, r, entries)
}
