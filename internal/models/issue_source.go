package models

import "fmt"

// IssueSource identifies the origin of an issue.
type IssueSource string

const (
	IssueSourceSentry IssueSource = "sentry"
	IssueSourceLinear IssueSource = "linear"
	IssueSourceManual IssueSource = "manual"
)

func (s IssueSource) Validate() error {
	switch s {
	case IssueSourceSentry, IssueSourceLinear, IssueSourceManual:
		return nil
	default:
		return fmt.Errorf("invalid IssueSource: %q", s)
	}
}
