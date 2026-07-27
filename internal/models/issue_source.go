package models

import "fmt"

// IssueSource identifies the origin of an issue.
type IssueSource string

const (
	IssueSourceSentry    IssueSource = "sentry"
	IssueSourceLinear    IssueSource = "linear"
	IssueSourcePagerDuty IssueSource = "pagerduty"
	IssueSourceManual    IssueSource = "manual"
	IssueSourceAgent     IssueSource = "agent"
	// IssueSourcePMAgent is retained only for historical records.
	IssueSourcePMAgent IssueSource = "pm_agent"
)

func (s IssueSource) Validate() error {
	switch s {
	case IssueSourceSentry, IssueSourceLinear, IssueSourcePagerDuty, IssueSourceManual, IssueSourceAgent, IssueSourcePMAgent:
		return nil
	default:
		return fmt.Errorf("invalid IssueSource: %q", s)
	}
}
