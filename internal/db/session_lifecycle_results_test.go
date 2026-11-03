package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The synthetic receipt deliberately panics on nil. Result validation must reject
// typed nil before invoking any receipt method.
type lifecycleContractReceipt struct {
	id, orgID uuid.UUID
	fence     AdmissionFence
}

func (v *lifecycleContractReceipt) validateLifecycleReceipt() error {
	if v.id == uuid.Nil {
		return errors.New("synthetic receipt has no identity")
	}
	return nil
}

func (v *lifecycleContractReceipt) lifecycleReceiptScope() (uuid.UUID, AdmissionFence) {
	return v.orgID, v.fence
}

func TestLifecycleResultContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                       string
		nilReceipt, typedNil, zeroReceipt, badEffect, defaultResult, invalid       bool
		wrongEffectOrg, wrongEffectSession, missingReceiptOrg, missingReceiptScope bool
	}{
		{name: "valid receipt and immutable notification list"},
		{name: "nil receipt", nilReceipt: true, invalid: true},
		{name: "typed nil receipt", typedNil: true, invalid: true},
		{name: "zero receipt", zeroReceipt: true, invalid: true},
		{name: "invalid notification", badEffect: true, invalid: true},
		{name: "zero result never success", defaultResult: true, invalid: true},
		{name: "notification cannot cross tenant", wrongEffectOrg: true, invalid: true},
		{name: "notification cannot cross session", wrongEffectSession: true, invalid: true},
		{name: "receipt cannot omit tenant", missingReceiptOrg: true, invalid: true},
		{name: "receipt cannot omit explicit scope", missingReceiptScope: true, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			fence := newLifecycleContractFence(t)
			var receipt lifecycleReceipt = &lifecycleContractReceipt{id: uuid.New(), orgID: orgID, fence: fence}
			if tt.nilReceipt {
				receipt = nil
			}
			if tt.typedNil {
				var typed *lifecycleContractReceipt
				receipt = typed
			}
			if tt.zeroReceipt {
				receipt = &lifecycleContractReceipt{}
			}
			sessionID := fence.SessionID()
			effect, err := newLifecycleEffect(LifecycleEffectWakeJobs, orgID, sessionID, nil)
			require.NoError(t, err, "construct a synthetic postcommit notification without executing effects")
			effects := []LifecycleEffect{effect}
			if tt.badEffect {
				effects[0] = LifecycleEffect{}
			}
			if tt.wrongEffectOrg {
				effects[0].orgID = uuid.New()
			}
			if tt.wrongEffectSession {
				effects[0].sessionID = uuid.New()
			}
			if tt.missingReceiptOrg {
				receipt.(*lifecycleContractReceipt).orgID = uuid.Nil
			}
			if tt.missingReceiptScope {
				receipt.(*lifecycleContractReceipt).fence = AdmissionFence{}
			}
			admission, admissionErr := newAdmissionResult(receipt, effects)
			settlement, settlementErr := newSettlementResult(receipt, ProjectionApplied, effects)
			if tt.defaultResult {
				admission = AdmissionResult[lifecycleReceipt]{}
				settlement = SettlementResult[lifecycleReceipt]{}
			}
			if tt.invalid && !tt.defaultResult {
				require.Error(t, admissionErr, "invalid receipt or effect cannot produce accepted admission")
				require.Equal(t, AdmissionResult[lifecycleReceipt]{}, admission, "failed admission construction exposes no receipt or effects")
				require.Error(t, settlementErr, "stale projection cannot excuse an invalid own receipt")
				require.Equal(t, SettlementResult[lifecycleReceipt]{}, settlement, "failed settlement construction exposes no partial own receipt")
			} else {
				require.NoError(t, admissionErr, "valid receipt shape may be constructed only by its canonical DB owner")
				require.NoError(t, settlementErr, "stale-shared-state disposition is valid only alongside a settled own receipt")
			}
			effects[0] = LifecycleEffect{}
			actual, notifications, err := admission.Unpack()
			own, disposition, settledNotifications, settleErr := settlement.Unpack()
			if tt.invalid {
				require.Error(t, err, "invalid or zero admission result cannot imply accepted work")
				require.Nil(t, actual, "invalid admission exposes no receipt")
				require.Nil(t, notifications, "invalid admission exposes no effects")
				require.Error(t, settleErr, "invalid or zero settlement cannot imply own outcome persistence")
				require.Nil(t, own, "invalid settlement exposes no own receipt")
				require.Equal(t, ProjectionDisposition(""), disposition, "invalid settlement has no stale-success disposition")
				require.Nil(t, settledNotifications, "invalid settlement exposes no effects")
				return
			}
			require.NoError(t, err, "a valid admission result remains usable after source-slice mutation")
			require.Equal(t, receipt, actual, "admission returns exactly its receipt")
			require.Equal(t, []LifecycleEffect{effect}, notifications, "caller mutation cannot replace retained effects")
			require.NoError(t, settleErr, "valid historical own settlement may preserve a successor's shared state")
			require.Equal(t, receipt, own, "settlement returns exactly its own receipt")
			require.Equal(t, ProjectionApplied, disposition, "only an applied projection returns current-state notifications")
			require.Equal(t, []LifecycleEffect{effect}, settledNotifications, "settlement preserves independent immutable effects")
			notifications[0] = LifecycleEffect{}
			_, again, err := admission.Unpack()
			require.NoError(t, err, "returning notifications must not expose internal storage")
			require.Equal(t, []LifecycleEffect{effect}, again, "each notification read returns a defensive copy")
		})
	}
}

func TestLifecycleEffectShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                          string
		kind                                          LifecycleEffectKind
		lane, zeroLane, zeroOrg, zeroSession, invalid bool
	}{
		{name: "job wake", kind: LifecycleEffectWakeJobs},
		{name: "session notification", kind: LifecycleEffectSessionChanged},
		{name: "thread notification", kind: LifecycleEffectThreadChanged, lane: true},
		{name: "inbox notification", kind: LifecycleEffectInboxChanged, lane: true},
		{name: "missing thread", kind: LifecycleEffectThreadChanged, invalid: true},
		{name: "missing inbox scope", kind: LifecycleEffectInboxChanged, invalid: true},
		{name: "zero lane", kind: LifecycleEffectThreadChanged, lane: true, zeroLane: true, invalid: true},
		{name: "missing tenant", kind: LifecycleEffectWakeJobs, zeroOrg: true, invalid: true},
		{name: "missing session", kind: LifecycleEffectWakeJobs, zeroSession: true, invalid: true},
		{name: "unknown effect", kind: "provider_call", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New()
			var lane *uuid.UUID
			if tt.lane {
				lane = &laneID
			}
			if tt.zeroLane {
				laneID = uuid.Nil
			}
			if tt.zeroOrg {
				orgID = uuid.Nil
			}
			if tt.zeroSession {
				sessionID = uuid.Nil
			}
			value, err := newLifecycleEffect(tt.kind, orgID, sessionID, lane)
			if tt.invalid {
				require.Error(t, err, "notifications require explicit valid identities and cannot invoke arbitrary operations")
				require.Equal(t, LifecycleEffect{}, value, "invalid notifications expose no partial scope")
				return
			}
			require.NoError(t, err, "a scoped notification is data only and should validate")
			require.Equal(t, tt.kind, value.Kind(), "notification kind is retained exactly")
			require.Equal(t, orgID, value.OrgID(), "tenant notification scope cannot be inferred")
			require.Equal(t, sessionID, value.SessionID(), "session notification scope cannot be inferred")
			actualLane, present := value.ThreadID()
			require.Equal(t, tt.lane, present, "notification preserves explicit lane presence")
			if tt.lane {
				require.Equal(t, laneID, actualLane, "notification retains its exact lane")
			}
		})
	}
}

func TestLifecycleSettlementEffectDisposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		projection      ProjectionDisposition
		notify, invalid bool
	}{
		{name: "applied projection may notify", projection: ProjectionApplied, notify: true},
		{name: "applied own receipt without notification", projection: ProjectionApplied},
		{name: "already applied has no effects", projection: ProjectionAlreadyApplied},
		{name: "stale shared state preserves own receipt only", projection: ProjectionStaleSharedState},
		{name: "already applied cannot repeat effects", projection: ProjectionAlreadyApplied, notify: true, invalid: true},
		{name: "stale shared state cannot wake current work", projection: ProjectionStaleSharedState, notify: true, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, fence := uuid.New(), newLifecycleContractFence(t)
			receipt := &lifecycleContractReceipt{id: uuid.New(), orgID: orgID, fence: fence}
			var effects []LifecycleEffect
			if tt.notify {
				effect, err := newLifecycleEffect(LifecycleEffectWakeJobs, orgID, fence.SessionID(), nil)
				require.NoError(t, err, "create scoped notification data to test projection-specific effect rules")
				effects = []LifecycleEffect{effect}
			}
			result, err := newSettlementResult(receipt, tt.projection, effects)
			if tt.invalid {
				require.Error(t, err, "stale or unchanged projection must not notify or schedule new current-state work")
				require.Equal(t, SettlementResult[*lifecycleContractReceipt]{}, result, "invalid projection/effect combination exposes no own receipt or effects")
				return
			}
			require.NoError(t, err, "valid own settlement can preserve its exact receipt independently of shared projection")
			own, projection, actualEffects, err := result.Unpack()
			require.NoError(t, err, "valid settlement receipt and projection remain independently observable")
			require.Equal(t, receipt, own, "stale shared state cannot lose or fabricate the settled own receipt")
			require.Equal(t, tt.projection, projection, "projection disposition remains explicit")
			require.Equal(t, effects, actualEffects, "only an applied projection may return generic notifications")
		})
	}
}
