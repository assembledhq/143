package models

import (
	"encoding/json"
	"fmt"
	"time"
)

type CodeReviewTrendStatus string

const (
	CodeReviewTrendAvailable   CodeReviewTrendStatus = "available"
	CodeReviewTrendUnavailable CodeReviewTrendStatus = "unavailable"
)

func (v CodeReviewTrendStatus) Validate() error {
	if v != CodeReviewTrendAvailable && v != CodeReviewTrendUnavailable {
		return fmt.Errorf("invalid code review trend status: %q", v)
	}
	return nil
}

type CodeReviewTrendMode string

const (
	CodeReviewTrendFinite  CodeReviewTrendMode = "finite"
	CodeReviewTrendAllTime CodeReviewTrendMode = "all_time"
)

func (v CodeReviewTrendMode) Validate() error {
	if v != CodeReviewTrendFinite && v != CodeReviewTrendAllTime {
		return fmt.Errorf("invalid code review trend mode: %q", v)
	}
	return nil
}

type CodeReviewTrendUnavailableReason string

const (
	CodeReviewTrendRangeTooLarge        CodeReviewTrendUnavailableReason = "range_too_large"
	CodeReviewTrendUnrepresentableRange CodeReviewTrendUnavailableReason = "unrepresentable_range"
	CodeReviewTrendInconsistentGeometry CodeReviewTrendUnavailableReason = "inconsistent_geometry"
)

func (v CodeReviewTrendUnavailableReason) Validate() error {
	switch v {
	case CodeReviewTrendRangeTooLarge, CodeReviewTrendUnrepresentableRange, CodeReviewTrendInconsistentGeometry:
		return nil
	}
	return fmt.Errorf("invalid code review trend unavailable reason: %q", v)
}

type CodeReviewTrendCurrentWindow struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	ObservedEnd time.Time `json:"observed_end"`
}
type CodeReviewTrendPreviousWindow struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	ComparedEnd time.Time `json:"compared_end"`
}
type CodeReviewTrendBucket struct {
	Start                   time.Time `json:"start"`
	End                     time.Time `json:"end"`
	PRsReviewed             int64     `json:"prs_reviewed"`
	ApprovedBy143           int64     `json:"approved_by_143"`
	MedianRoundsToApproval  *float64  `json:"median_rounds_to_approval"`
	AverageRoundsToApproval *float64  `json:"average_rounds_to_approval"`
	P95RoundsToApproval     *float64  `json:"p95_rounds_to_approval"`
	Partial                 bool      `json:"partial"`
	OverflowPRs             int64     `json:"overflow_prs"`
}
type CodeReviewTrendPoint struct {
	Index           int                    `json:"index"`
	Current         CodeReviewTrendBucket  `json:"current"`
	Previous        *CodeReviewTrendBucket `json:"previous"`
	UnequalExposure bool                   `json:"unequal_exposure"`
}

// CodeReviewTrend uses a status discriminator. Unavailable responses omit all
// geometry; available empty All time responses retain null windows and [].
type CodeReviewTrend struct {
	Status                         CodeReviewTrendStatus            `json:"status"`
	GeneratedAt                    time.Time                        `json:"generated_at"`
	Mode                           CodeReviewTrendMode              `json:"mode,omitempty"`
	UnavailableReason              CodeReviewTrendUnavailableReason `json:"unavailable_reason,omitempty"`
	BucketWidthSeconds             int64                            `json:"bucket_width_seconds"`
	CurrentWindow                  *CodeReviewTrendCurrentWindow    `json:"current_window"`
	PreviousWindow                 *CodeReviewTrendPreviousWindow   `json:"previous_window"`
	ObservedEndAdvancedByData      bool                             `json:"observed_end_advanced_by_data"`
	OverflowPRs                    int64                            `json:"overflow_prs"`
	LatestIncludedFirstRequestedAt *time.Time                       `json:"latest_included_first_requested_at"`
	Points                         []CodeReviewTrendPoint           `json:"points"`
}

func (v CodeReviewTrend) MarshalJSON() ([]byte, error) {
	if v.Status == CodeReviewTrendUnavailable {
		return json.Marshal(struct {
			Status            CodeReviewTrendStatus            `json:"status"`
			GeneratedAt       time.Time                        `json:"generated_at"`
			UnavailableReason CodeReviewTrendUnavailableReason `json:"unavailable_reason"`
		}{v.Status, v.GeneratedAt, v.UnavailableReason})
	}
	type wire CodeReviewTrend
	return json.Marshal(wire(v))
}
func (v CodeReviewTrend) Validate() error {
	if err := v.Status.Validate(); err != nil {
		return err
	}
	if v.Status == CodeReviewTrendUnavailable {
		return v.UnavailableReason.Validate()
	}
	return v.Mode.Validate()
}
