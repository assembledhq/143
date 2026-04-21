	"github.com/google/uuid"
)

// automationNameMaxLength and automationGoalMaxLength mirror the
// chk_automations_name_length and chk_automations_goal_length CHECK
// constraints so oversized requests fail with a user-facing 400.
const (
	automationNameMaxLength = 200
	automationGoalMaxLength = 4000
	return nil
}

// validateBaseBranch rejects obvious invalid refs while leaving full git ref
// validation to checkout.
func validateBaseBranch(b string) error {
	trimmed := strings.TrimSpace(b)
	if trimmed == "" {
	return nil
}

// validateTimezone rejects malformed locations at write time.
func validateTimezone(tz string) error {
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid timezone %q", tz)
	return nil
}

// resolveRepositoryID verifies repository_id belongs to orgID.
//
// Fails closed when no repo store is configured: the router always calls
// SetRepositoryStore, so a missing store means a wiring bug.
func (h *AutomationHandler) resolveRepositoryID(ctx context.Context, orgID uuid.UUID, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
