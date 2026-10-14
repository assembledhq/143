package models

import "fmt"

// ReviewFeedbackCategory is the database-backed classification of review feedback.
type ReviewFeedbackCategory string

const (
	ReviewFeedbackStyle         ReviewFeedbackCategory = "style"
	ReviewFeedbackLogicBug      ReviewFeedbackCategory = "logic_bug"
	ReviewFeedbackEdgeCase      ReviewFeedbackCategory = "edge_case"
	ReviewFeedbackWrongApproach ReviewFeedbackCategory = "wrong_approach"
	ReviewFeedbackMissingTest   ReviewFeedbackCategory = "missing_test"
	ReviewFeedbackSecurity      ReviewFeedbackCategory = "security"
	ReviewFeedbackPerformance   ReviewFeedbackCategory = "performance"
	ReviewFeedbackNit           ReviewFeedbackCategory = "nit"
)

func (c ReviewFeedbackCategory) Validate() error {
	switch c {
	case ReviewFeedbackStyle, ReviewFeedbackLogicBug, ReviewFeedbackEdgeCase, ReviewFeedbackWrongApproach,
		ReviewFeedbackMissingTest, ReviewFeedbackSecurity, ReviewFeedbackPerformance, ReviewFeedbackNit:
		return nil
	default:
		return fmt.Errorf("invalid review feedback category %q", c)
	}
}
