package models

import "fmt"

type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusRunning    JobStatus = "running"
	JobStatusSucceeded  JobStatus = "succeeded"
	JobStatusFailed     JobStatus = "failed"
	JobStatusCancelled  JobStatus = "cancelled"
	JobStatusDeadLetter JobStatus = "dead_letter"
)

func (s JobStatus) Validate() error {
	switch s {
	case JobStatusPending, JobStatusRunning, JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusDeadLetter:
		return nil
	default:
		return fmt.Errorf("invalid JobStatus: %q", s)
	}
}

// ReleaseChannel routes execution between the canary plane (latest main,
// dogfood orgs) and the stable plane (pinned releases, customer orgs). Orgs
// carry a release_channel; jobs are stamped with it at enqueue; workers claim
// only jobs matching their configured channel. See
// docs/design/future/118-canary-stable-release-channels.md.
type ReleaseChannel string

const (
	ReleaseChannelStable ReleaseChannel = "stable"
	ReleaseChannelCanary ReleaseChannel = "canary"
)

func (c ReleaseChannel) Validate() error {
	switch c {
	case ReleaseChannelStable, ReleaseChannelCanary:
		return nil
	default:
		return fmt.Errorf("invalid ReleaseChannel: %q", c)
	}
}
