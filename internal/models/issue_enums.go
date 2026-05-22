package models

import "fmt"

type IssueStatus string

const (
	IssueStatusOpen       IssueStatus = "open"
	IssueStatusTriaged    IssueStatus = "triaged"
	IssueStatusInProgress IssueStatus = "in_progress"
	IssueStatusFixed      IssueStatus = "fixed"
	IssueStatusWontFix    IssueStatus = "wont_fix"
	IssueStatusDuplicate  IssueStatus = "duplicate"
)

func (s IssueStatus) Validate() error {
	switch s {
	case IssueStatusOpen, IssueStatusTriaged, IssueStatusInProgress, IssueStatusFixed, IssueStatusWontFix, IssueStatusDuplicate:
		return nil
	default:
		return fmt.Errorf("invalid IssueStatus: %q", s)
	}
}

type IssueSeverity string

const (
	IssueSeverityCritical IssueSeverity = "critical"
	IssueSeverityHigh     IssueSeverity = "high"
	IssueSeverityMedium   IssueSeverity = "medium"
	IssueSeverityLow      IssueSeverity = "low"
)

func (s IssueSeverity) Validate() error {
	switch s {
	case "", IssueSeverityCritical, IssueSeverityHigh, IssueSeverityMedium, IssueSeverityLow:
		return nil
	default:
		return fmt.Errorf("invalid IssueSeverity: %q", s)
	}
}

func (s *IssueSeverity) Scan(src any) error {
	if src == nil {
		*s = ""
		return nil
	}
	switch v := src.(type) {
	case string:
		*s = IssueSeverity(v)
	case []byte:
		*s = IssueSeverity(string(v))
	default:
		return fmt.Errorf("scan IssueSeverity from %T", src)
	}
	return nil
}
