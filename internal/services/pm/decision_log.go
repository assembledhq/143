package pm

import "github.com/assembledhq/143/internal/models"

func planToDecisionLog(plan *Plan) []models.PMDecisionLogEntry {
	if plan == nil {
		return nil
	}

	var entries []models.PMDecisionLogEntry

	for _, task := range plan.Tasks {
		for _, issueID := range task.IssueIDs {
			id := issueID
			entries = append(entries, models.PMDecisionLogEntry{
				PlanID:    plan.ID,
				OrgID:     plan.OrgID,
				IssueID:   &id,
				Decision:  models.PMDecisionTypeDelegate,
				Reasoning: task.Reasoning,
			})
		}
	}

	for _, skip := range plan.SkippedIssues {
		issueID := skip.IssueID
		entries = append(entries, models.PMDecisionLogEntry{
			PlanID:    plan.ID,
			OrgID:     plan.OrgID,
			IssueID:   &issueID,
			Decision:  models.PMDecisionTypeSkip,
			Reasoning: skip.Detail,
		})
	}

	for _, cluster := range plan.Clusters {
		for _, issueID := range cluster.IssueIDs {
			id := issueID
			entries = append(entries, models.PMDecisionLogEntry{
				PlanID:    plan.ID,
				OrgID:     plan.OrgID,
				IssueID:   &id,
				Decision:  models.PMDecisionTypeCluster,
				Reasoning: cluster.RootCause + " — " + cluster.Strategy,
			})
		}
	}

	return entries
}
