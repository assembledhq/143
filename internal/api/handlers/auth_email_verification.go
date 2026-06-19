package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/domains"
)

// sendEmailVerificationFor issues a fresh verification token for the user
// and emails the confirm link. Best-effort: failures are logged, never
// surfaced — signup and resend UX must not break because SMTP hiccuped.
// No-op when the verification store isn't wired.
func (h *AuthHandler) sendEmailVerificationFor(r *http.Request, user *models.User) {
	if h.emailVerifications == nil || user == nil {
		return
	}
	log := zerolog.Ctx(r.Context())

	token, err := generateRandomString(32)
	if err != nil {
		log.Warn().Err(err).Msg("failed to generate email verification token")
		return
	}
	rec := &models.EmailVerificationToken{
		UserID:    user.ID,
		Email:     user.Email,
		Token:     token,
		ExpiresAt: time.Now().Add(db.EmailVerificationTokenTTL),
	}
	if err := h.emailVerifications.Create(r.Context(), rec); err != nil {
		log.Warn().Err(err).Msg("failed to persist email verification token")
		return
	}

	// generateRandomString is hex-encoded, so the token is URL-safe to
	// concatenate (same invariant as the invitation accept link).
	verifyURL := h.cfg.FrontendURL + "/verify-email?token=" + token
	if h.emailSender == nil {
		log.Info().Str("to", user.Email).Str("verify_url", verifyURL).
			Msg("email sending skipped (SMTP not configured)")
		return
	}
	if err := h.emailSender.SendEmailVerification(r.Context(), user.Email, verifyURL); err != nil {
		log.Warn().Err(err).Msg("failed to send verification email")
	}
}

// SendEmailVerification (re)sends the verification link to the signed-in
// user's own address.
// POST /api/v1/auth/email-verifications (authed, rate-limited)
func (h *AuthHandler) SendEmailVerification(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	if h.emailVerifications == nil {
		writeError(w, r, http.StatusServiceUnavailable, "VERIFICATION_UNAVAILABLE", "email verification is not configured on this server")
		return
	}

	if verifiedAt, err := h.userStore.GetEmailVerifiedAt(r.Context(), user.ID); err == nil && verifiedAt != nil {
		writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{Data: map[string]bool{"already_verified": true}})
		return
	}

	h.sendEmailVerificationFor(r, user)
	w.WriteHeader(http.StatusAccepted)
}

// confirmEmailVerificationResponse is the confirm endpoint's payload.
// JoinedOrg is set when verifying also auto-joined a captured-domain org,
// so the frontend can switch straight into the team workspace.
type confirmEmailVerificationResponse struct {
	Verified  bool                         `json:"verified"`
	JoinedOrg *models.JoinableOrganization `json:"joined_org,omitempty"`
}

// ConfirmEmailVerification consumes a verification token, stamps the user's
// email as verified, and — when the email's domain is captured by an org
// with auto-join enabled — grants membership on the spot. Public route: the
// link may be opened on a device with no session; the token alone is the
// credential (single-use, 24h expiry, rate-limited lookups).
// POST /api/v1/auth/email-verifications/confirm
func (h *AuthHandler) ConfirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	if h.emailVerifications == nil {
		writeError(w, r, http.StatusServiceUnavailable, "VERIFICATION_UNAVAILABLE", "email verification is not configured on this server")
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "token is required")
		return
	}

	tok, err := h.emailVerifications.Consume(r.Context(), strings.TrimSpace(body.Token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Invalid, expired, superseded, and already-used all collapse to
			// one answer so the endpoint can't be used as a token oracle.
			writeError(w, r, http.StatusGone, "VERIFICATION_INVALID", "This verification link is invalid or has expired. Request a new one and try again.")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to confirm verification", err)
		return
	}

	user, err := h.userStore.GetByIDGlobal(r.Context(), tok.UserID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load account", err)
		return
	}
	// The token proves receipt at the snapshotted address; if the account's
	// email changed since issue (e.g. an OAuth link rewrote it), the proof
	// no longer applies to the current identity.
	if !strings.EqualFold(user.Email, tok.Email) {
		writeError(w, r, http.StatusGone, "VERIFICATION_INVALID", "This verification link no longer matches the email on your account. Request a new one and try again.")
		return
	}

	if err := h.userStore.SetEmailVerification(r.Context(), user.ID, tok.Email, true); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to record verification", err)
		return
	}

	resp := confirmEmailVerificationResponse{Verified: true}
	if target, joined := h.autoJoinAfterEmailVerification(r, &user); joined {
		resp.JoinedOrg = target
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[confirmEmailVerificationResponse]{Data: resp})
}

// autoJoinAfterEmailVerification grants the freshly-verified user a member
// seat in the org capturing their email domain, mirroring the OAuth signup
// capture. Best-effort: a failure leaves them verified with the joinable
// surface as fallback, never a failed confirmation.
func (h *AuthHandler) autoJoinAfterEmailVerification(r *http.Request, user *models.User) (*models.JoinableOrganization, bool) {
	if h.orgDomains == nil || h.memberships == nil {
		return nil, false
	}
	log := zerolog.Ctx(r.Context())

	emailDomain := domains.EmailDomain(user.Email)
	if emailDomain == "" || domains.IsPublicEmailDomain(emailDomain) {
		return nil, false
	}
	target, err := h.orgDomains.FindAutoJoinOrgByDomain(r.Context(), emailDomain)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Warn().Err(err).Msg("auto-join lookup failed after email verification")
		}
		return nil, false
	}
	// Already a member (e.g. previously invited) — nothing to join, and
	// reporting a "joined" event for a no-op grant would mislead the user
	// and the audit trail.
	if _, err := h.memberships.Get(r.Context(), user.ID, target.OrgID); err == nil {
		return nil, false
	}

	effectiveRole, err := h.memberships.GrantAtLeast(r.Context(), user.ID, target.OrgID, models.RoleMember)
	if err != nil {
		log.Warn().Err(err).Str("org_id", target.OrgID.String()).Msg("auto-join grant failed after email verification")
		return nil, false
	}
	if err := h.userStore.UpdateLastOrgID(r.Context(), user.ID, &target.OrgID); err != nil {
		log.Warn().Err(err).Msg("failed to set last org after verification auto-join")
	}

	if h.audit != nil {
		userIDStr := user.ID.String()
		details, _ := json.Marshal(map[string]string{"domain": target.Domain, "role": effectiveRole, "email": user.Email, "via": "email_verification"})
		params := db.UserActionParams{
			OrgID:        target.OrgID,
			UserID:       user.ID,
			Action:       models.AuditActionTeamMemberAutoJoined,
			ResourceType: models.AuditResourceTeamMember,
			ResourceID:   &userIDStr,
			Details:      details,
		}
		if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
			params.RequestID = &reqID
		}
		if ua := r.UserAgent(); ua != "" {
			params.UserAgent = &ua
		}
		if ip := parseClientIP(r); ip != nil {
			params.IPAddress = ip
		}
		h.audit.EmitUserAction(r.Context(), params)
	}

	return &target, true
}
