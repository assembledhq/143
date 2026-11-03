package db

import (
	"errors"
	"math"
	"slices"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// MessageAdmissionDisposition describes accepted input, never provider admission.
type MessageAdmissionDisposition string

const (
	MessageAdmissionNewJob   MessageAdmissionDisposition = "new_job"
	MessageAdmissionQueued   MessageAdmissionDisposition = "queued"
	MessageAdmissionWaiting  MessageAdmissionDisposition = "waiting"
	MessageAdmissionReplayed MessageAdmissionDisposition = "replayed"
)

func (d MessageAdmissionDisposition) Validate() error {
	switch d {
	case MessageAdmissionNewJob, MessageAdmissionQueued, MessageAdmissionWaiting, MessageAdmissionReplayed:
		return nil
	default:
		return errors.New("invalid message admission disposition")
	}
}

// Admission is sealed DB-produced receipt data. It neither attests outer commit
// nor grants a runtime lease, start permit, provider write or current authority.
// A replay retains the original accepted scope/options, not today's eligibility.
type Admission struct {
	orgID        uuid.UUID
	fence        AdmissionFence
	ingress      LifecycleIngress
	dispatchID   uuid.UUID
	disposition  MessageAdmissionDisposition
	original     MessageAdmissionDisposition
	message      models.SessionMessage
	inbox        models.ThreadInboxEntry
	comments     []models.SessionReviewComment
	jobID        uuid.UUID
	authority    AdmissionAuthority
	options      MessageAdmissionDispatchOptions
	holderID     *uuid.UUID
	targetNodeID *string
	threadState  models.ThreadStatus
	reason       string
	valid        bool
}

func (a Admission) validateLifecycleReceipt() error {
	if !a.valid || a.orgID == uuid.Nil || a.dispatchID == uuid.Nil {
		return errors.New("message admission receipt is not initialized")
	}
	if err := a.fence.Validate(); err != nil {
		return err
	}
	if err := a.ingress.Validate(); err != nil {
		return err
	}
	if err := a.disposition.Validate(); err != nil {
		return err
	}
	if err := a.original.Validate(); err != nil {
		return err
	}
	if a.original == MessageAdmissionReplayed || (a.disposition != MessageAdmissionReplayed && a.disposition != a.original) {
		return errors.New("message admission receipt has inconsistent original outcome")
	}
	if err := a.options.Validate(); err != nil {
		return err
	}
	if err := a.threadState.Validate(); err != nil {
		return err
	}
	lane, threaded := a.fence.ThreadID()
	if !threaded || a.message.ID <= 0 || a.message.TurnNumber <= 0 || a.message.TurnNumber > math.MaxInt32 || a.message.OrgID != a.orgID || a.message.SessionID != a.fence.SessionID() || a.message.ThreadID == nil || *a.message.ThreadID != lane || a.message.Role != models.MessageRoleUser {
		return errors.New("message admission receipt has a different message scope")
	}
	if a.inbox.ID == uuid.Nil || a.inbox.OrgID != a.orgID || a.inbox.SessionID != a.fence.SessionID() || a.inbox.ThreadID != lane || a.inbox.MessageID != a.message.ID || a.inbox.EntryType != models.ThreadInboxEntryTypeUserMessage {
		return errors.New("message admission receipt has a different inbox scope")
	}
	if a.original == MessageAdmissionNewJob {
		if a.jobID == uuid.Nil || a.authority.Validate() != nil || a.authority.orgID != a.orgID || a.authority.dispatchID != a.dispatchID || a.authority.fence != a.fence || a.authority.ingress != a.ingress || a.authority.turn != a.message.TurnNumber {
			return errors.New("message admission job receipt has no exact claim association")
		}
	} else if a.jobID != uuid.Nil || a.authority != (AdmissionAuthority{}) {
		return errors.New("waiting input cannot manufacture a job or logical claim")
	}
	if a.original != MessageAdmissionNewJob && len(a.comments) != 0 {
		return errors.New("queued or waiting ordinary input cannot resolve comments")
	}
	if a.holderID != nil && *a.holderID == uuid.Nil {
		return errors.New("message admission receipt has a zero holder")
	}
	for _, c := range a.comments {
		if c.ID == uuid.Nil || c.OrgID != a.orgID || c.SessionID != a.fence.SessionID() || !c.Resolved {
			return errors.New("message admission receipt has a different comment scope")
		}
	}
	return nil
}

// Validate checks only sealed receipt shape. It is not proof of outer commit,
// current membership, current epochs, claim ownership or execution permission.
func (a Admission) Validate() error { return a.validateLifecycleReceipt() }

func (a Admission) lifecycleReceiptScope() (uuid.UUID, AdmissionFence) { return a.orgID, a.fence }
func (a Admission) Disposition() MessageAdmissionDisposition           { return a.disposition }
func (a Admission) OriginalDisposition() MessageAdmissionDisposition   { return a.original }
func (a Admission) OrgID() uuid.UUID                                   { return a.orgID }
func (a Admission) Fence() AdmissionFence                              { return a.fence }
func (a Admission) Ingress() LifecycleIngress                          { return a.ingress }
func (a Admission) DispatchID() uuid.UUID                              { return a.dispatchID }
func (a Admission) Message() models.SessionMessage                     { return cloneMessageAdmissionMessage(a.message) }
func (a Admission) InboxEntry() models.ThreadInboxEntry                { return cloneMessageAdmissionInbox(a.inbox) }
func (a Admission) ResolvedComments() []models.SessionReviewComment {
	return cloneMessageAdmissionComments(a.comments)
}
func (a Admission) JobID() (uuid.UUID, bool) { return a.jobID, a.jobID != uuid.Nil }
func (a Admission) Authority() (AdmissionAuthority, bool) {
	return a.authority, a.original == MessageAdmissionNewJob && a.valid
}
func (a Admission) DispatchOptions() MessageAdmissionDispatchOptions { return a.options }
func (a Admission) TargetNodeID() *string                            { return cloneMessageAdmissionPointer(a.targetNodeID) }
func (a Admission) LeaseHolderID() (uuid.UUID, bool) {
	if a.holderID == nil {
		return uuid.Nil, false
	}
	return *a.holderID, true
}
func (a Admission) ThreadStatus() models.ThreadStatus { return a.threadState }
func (a Admission) Reason() string                    { return a.reason }

func cloneMessageAdmissionMessage(m models.SessionMessage) models.SessionMessage {
	m.ThreadID = cloneMessageAdmissionPointer(m.ThreadID)
	m.UserID = cloneMessageAdmissionPointer(m.UserID)
	m.ActivityPhaseID = cloneMessageAdmissionPointer(m.ActivityPhaseID)
	m.AutomationRunID = cloneMessageAdmissionPointer(m.AutomationRunID)
	m.Attachments = slices.Clone(m.Attachments)
	m.References = slices.Clone(m.References)
	m.Commands = slices.Clone(m.Commands)
	m.TokenUsage = slices.Clone(m.TokenUsage)
	return m
}

func cloneMessageAdmissionInbox(e models.ThreadInboxEntry) models.ThreadInboxEntry {
	e.ClientMessageID = cloneMessageAdmissionPointer(e.ClientMessageID)
	e.LastError = cloneMessageAdmissionPointer(e.LastError)
	e.OwnerNodeID = cloneMessageAdmissionPointer(e.OwnerNodeID)
	e.RuntimeID = cloneMessageAdmissionPointer(e.RuntimeID)
	e.DeliveredAt = cloneMessageAdmissionPointer(e.DeliveredAt)
	e.AckedAt = cloneMessageAdmissionPointer(e.AckedAt)
	e.AppliedAt = cloneMessageAdmissionPointer(e.AppliedAt)
	e.Payload = slices.Clone(e.Payload)
	return e
}

func cloneMessageAdmissionComments(comments []models.SessionReviewComment) []models.SessionReviewComment {
	result := slices.Clone(comments)
	for i := range result {
		result[i].ResolvedAt = cloneMessageAdmissionPointer(result[i].ResolvedAt)
		result[i].ResolvedByPass = cloneMessageAdmissionPointer(result[i].ResolvedByPass)
	}
	return result
}
