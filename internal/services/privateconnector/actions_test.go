package privateconnector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/connector"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeActionGateway struct {
	req       connector.ActionRequest
	signature string
	result    connector.ActionResult
	err       error
}

func (g *fakeActionGateway) DispatchPrivateConnectorAction(_ context.Context, req connector.ActionRequest, signature string) (connector.ActionResult, error) {
	g.req = req
	g.signature = signature
	if g.err != nil {
		return connector.ActionResult{}, g.err
	}
	return g.result, nil
}

func TestServiceDispatchPrivateConnectorActionSignsRecordsAndCompletes(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	gateway := &fakeActionGateway{result: connector.ActionResult{
		Payload:  json.RawMessage(`{"entries":[]}`),
		Metadata: connector.ActionMetadata{ResultCount: 7, DurationMs: 35},
	}}
	svc := NewService(store, Config{
		Now:              func() time.Time { return now },
		ActionSigningKey: privateKey,
		ActionGateway:    gateway,
	})
	orgID := uuid.New()
	connectorID := uuid.New()
	resourceID := uuid.New()

	payload, err := svc.DispatchPrivateConnectorAction(context.Background(), integration.PrivateConnectorActionDispatchRequest{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceID:  resourceID,
		Capability:  "victorialogs.query",
		Params:      json.RawMessage(`{"query":"service:api"}`),
	})

	require.NoError(t, err, "DispatchPrivateConnectorAction should return gateway payload on success")
	require.JSONEq(t, `{"entries":[]}`, string(payload), "DispatchPrivateConnectorAction should return the action payload")
	require.Equal(t, orgID, gateway.req.OrgID, "signed action should preserve org scope")
	require.Equal(t, connectorID, gateway.req.ConnectorID, "signed action should preserve connector scope")
	require.Equal(t, resourceID, gateway.req.ResourceID, "signed action should preserve resource scope")
	require.Equal(t, "victorialogs.query", gateway.req.Capability, "signed action should preserve capability")
	require.Equal(t, now, gateway.req.IssuedAt, "signed action should use service clock for issued_at")
	require.Equal(t, now.Add(30*time.Second), gateway.req.ExpiresAt, "signed action should expire quickly")
	require.NotEmpty(t, gateway.signature, "DispatchPrivateConnectorAction should sign gateway requests")
	require.NoError(t, connector.VerifyActionRequest(privateKey.Public().(ed25519.PublicKey), gateway.req, gateway.signature, connector.VerifyOptions{
		OrgID:       orgID,
		ConnectorID: connectorID,
		ResourceIDs: map[uuid.UUID]struct{}{resourceID: {}},
		Now:         func() time.Time { return now },
	}), "gateway signature should verify against the canonical action request")
	require.Equal(t, orgID, store.recordedAction.OrgID, "action record should preserve org scope")
	require.Equal(t, connectorID, store.recordedAction.ConnectorGroupID, "action record should preserve connector group scope")
	require.Equal(t, resourceID, store.recordedAction.ResourceID, "action record should preserve resource scope")
	require.Equal(t, gateway.req.RequestID.String(), store.recordedAction.RequestNonce, "action record should store the request nonce")
	require.NotContains(t, store.recordedAction.RequestFingerprint, "service:api", "action record should store a fingerprint instead of raw params")
	require.Equal(t, models.PrivateConnectorActionStatusSucceeded, store.completedStatus, "successful dispatch should mark the action succeeded")
	require.Equal(t, 7, *store.completedResultCount, "successful dispatch should record result count metadata")
	require.Equal(t, 35, *store.completedDurationMs, "successful dispatch should record duration metadata")
}

func TestServiceDispatchPrivateConnectorActionMarksGatewayFailures(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	store := &fakeStore{}
	gateway := &fakeActionGateway{err: errors.New("connector offline")}
	svc := NewService(store, Config{
		ActionSigningKey: privateKey,
		ActionGateway:    gateway,
	})

	_, err = svc.DispatchPrivateConnectorAction(context.Background(), integration.PrivateConnectorActionDispatchRequest{
		OrgID:       uuid.New(),
		ConnectorID: uuid.New(),
		ResourceID:  uuid.New(),
		Capability:  "victorialogs.query",
		Params:      json.RawMessage(`{"query":"service:api"}`),
	})

	require.Error(t, err, "DispatchPrivateConnectorAction should return gateway errors")
	require.Equal(t, models.PrivateConnectorActionStatusFailed, store.completedStatus, "failed dispatch should mark the action failed")
	require.NotNil(t, store.completedErrorMessage, "failed dispatch should persist an operator-visible error message")
	require.Contains(t, *store.completedErrorMessage, "connector offline", "failed dispatch should include the gateway error")
}

func TestServiceDispatchPrivateConnectorActionRequiresGateway(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeStore{}, Config{})

	_, err := svc.DispatchPrivateConnectorAction(context.Background(), integration.PrivateConnectorActionDispatchRequest{
		OrgID:       uuid.New(),
		ConnectorID: uuid.New(),
		ResourceID:  uuid.New(),
		Capability:  "victorialogs.query",
	})

	require.ErrorIs(t, err, ErrActionDispatchUnavailable, "DispatchPrivateConnectorAction should not register tools without a gateway dispatcher")
}

func TestPrivateConnectorActionFingerprintNormalizesPostgresPredicateValues(t *testing.T) {
	t.Parallel()

	base := connector.ActionRequest{
		OrgID:       uuid.New(),
		ConnectorID: uuid.New(),
		ResourceID:  uuid.New(),
		Capability:  "postgres.query",
	}
	first := base
	first.Params = json.RawMessage(`{"query":"SELECT id FROM users WHERE email = 'dev@example.com' AND attempts > 3"}`)
	second := base
	second.Params = json.RawMessage(`{"query":" select id from users where email = 'ops@example.com' and attempts > 99 "}`)
	otherTable := base
	otherTable.Params = json.RawMessage(`{"query":"SELECT id FROM invoices WHERE email = 'dev@example.com' AND attempts > 3"}`)

	require.Equal(t, privateConnectorActionFingerprint(first), privateConnectorActionFingerprint(second), "Postgres fingerprints should ignore predicate literal values")
	require.NotEqual(t, privateConnectorActionFingerprint(first), privateConnectorActionFingerprint(otherTable), "Postgres fingerprints should preserve affected statement shape")
}

func TestServiceRequestIdentityRotationDispatchesConnectorControlAction(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	now := time.Date(2026, 6, 20, 13, 0, 0, 0, time.UTC)
	gateway := &fakeActionGateway{result: connector.ActionResult{Payload: json.RawMessage(`{"public_key":"new"}`)}}
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{
		Now:              func() time.Time { return now },
		ActionSigningKey: privateKey,
		ActionGateway:    gateway,
	})

	instance, err := svc.RequestIdentityRotation(context.Background(), orgID, instanceID)

	require.NoError(t, err, "RequestIdentityRotation should dispatch to the connector")
	require.Equal(t, instanceID, instance.ID, "RequestIdentityRotation should return the refreshed instance")
	require.Equal(t, orgID, gateway.req.OrgID, "rotation action should preserve org scope")
	require.Equal(t, groupID, gateway.req.ConnectorID, "rotation action should target the connector group")
	require.Equal(t, connector.ConnectorControlResourceID, gateway.req.ResourceID, "rotation action should use the connector control resource")
	require.Equal(t, connector.CapabilityRotateIdentity, gateway.req.Capability, "rotation action should use the rotate identity capability")
	require.JSONEq(t, `{"instance_id":"`+instanceID.String()+`"}`, string(gateway.req.Params), "rotation action should identify the requested instance")
	require.NoError(t, connector.VerifyActionRequest(privateKey.Public().(ed25519.PublicKey), gateway.req, gateway.signature, connector.VerifyOptions{
		OrgID:       orgID,
		ConnectorID: groupID,
		ResourceIDs: map[uuid.UUID]struct{}{connector.ConnectorControlResourceID: {}},
		Now:         func() time.Time { return now },
	}), "rotation action should be signed for connector verification")
}

func TestServiceRequestConfigReloadDispatchesConnectorControlAction(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	gateway := &fakeActionGateway{result: connector.ActionResult{Payload: json.RawMessage(`{"reloaded":true}`)}}
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{
		ActionSigningKey: privateKey,
		ActionGateway:    gateway,
	})

	instance, err := svc.RequestConfigReload(context.Background(), orgID, instanceID)

	require.NoError(t, err, "RequestConfigReload should dispatch to the connector")
	require.Equal(t, instanceID, instance.ID, "RequestConfigReload should return the refreshed instance")
	require.Equal(t, connector.ConnectorControlResourceID, gateway.req.ResourceID, "reload action should use the connector control resource")
	require.Equal(t, connector.CapabilityReloadConfig, gateway.req.Capability, "reload action should use the reload config capability")
}

func TestServiceRequestConnectorUpdateDispatchesConnectorControlAction(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	orgID := uuid.New()
	groupID := uuid.New()
	instanceID := uuid.New()
	gateway := &fakeActionGateway{result: connector.ActionResult{Payload: json.RawMessage(`{"started":true}`)}}
	store := &fakeStore{instance: models.PrivateConnectorInstance{
		ID:               instanceID,
		OrgID:            orgID,
		ConnectorGroupID: groupID,
		Status:           models.PrivateConnectorInstanceStatusOnline,
	}}
	svc := NewService(store, Config{
		ActionSigningKey: privateKey,
		ActionGateway:    gateway,
	})

	instance, err := svc.RequestConnectorUpdate(context.Background(), orgID, instanceID)

	require.NoError(t, err, "RequestConnectorUpdate should dispatch to the connector")
	require.Equal(t, instanceID, instance.ID, "RequestConnectorUpdate should return the refreshed instance")
	require.Equal(t, connector.ConnectorControlResourceID, gateway.req.ResourceID, "update action should use the connector control resource")
	require.Equal(t, connector.CapabilityTriggerUpdate, gateway.req.Capability, "update action should use the update capability")
}

func TestServiceTestResourceProbesVictoriaLogsAndRecordsHealth(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		resource: models.PrivateConnectorResource{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			ResourceType:     models.PrivateConnectorResourceTypeVictoriaLogs,
			Mode:             models.PrivateConnectorResourceModeLogs,
			Status:           models.PrivateConnectorResourceStatusConfigured,
		},
	}
	gateway := &fakeActionGateway{result: connector.ActionResult{
		Payload:  json.RawMessage(`{"fields":[]}`),
		Metadata: connector.ActionMetadata{FieldCount: 0, DurationMs: 12},
	}}
	svc := NewService(store, Config{ActionSigningKey: privateKey, ActionGateway: gateway})

	resource, err := svc.TestResource(context.Background(), orgID, resourceID)

	require.NoError(t, err, "TestResource should return a ready resource after a successful probe")
	require.Equal(t, models.PrivateConnectorResourceStatusReady, resource.Status, "TestResource should mark successful resources ready")
	require.NotNil(t, resource.LastTestStatus, "TestResource should record a successful test status")
	require.Equal(t, "success", *resource.LastTestStatus, "TestResource should store success test status")
	require.Equal(t, groupID, gateway.req.ConnectorID, "TestResource should dispatch through the resource connector")
	require.Equal(t, resourceID, gateway.req.ResourceID, "TestResource should dispatch to the resource id")
	require.Equal(t, "victorialogs.fields", gateway.req.Capability, "TestResource should use a bounded VictoriaLogs field discovery probe")
	require.JSONEq(t, `{"limit":1,"since":"5m"}`, string(gateway.req.Params), "TestResource should send bounded probe params")
}

func TestServiceTestResourceProbesPostgres(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err, "test signing key should generate")
	orgID := uuid.New()
	groupID := uuid.New()
	resourceID := uuid.New()
	store := &fakeStore{
		resource: models.PrivateConnectorResource{
			ID:               resourceID,
			OrgID:            orgID,
			ConnectorGroupID: groupID,
			ResourceType:     models.PrivateConnectorResourceTypePostgres,
			Mode:             models.PrivateConnectorResourceModeAgentReadOnly,
			Status:           models.PrivateConnectorResourceStatusConfigured,
		},
	}
	gateway := &fakeActionGateway{result: connector.ActionResult{Payload: json.RawMessage(`{"rows":[{"?column?":1}]}`)}}
	svc := NewService(store, Config{ActionSigningKey: privateKey, ActionGateway: gateway})

	resource, err := svc.TestResource(context.Background(), orgID, resourceID)

	require.NoError(t, err, "TestResource should return a ready Postgres resource after a successful probe")
	require.Equal(t, models.PrivateConnectorResourceStatusReady, resource.Status, "TestResource should mark successful Postgres resources ready")
	require.Equal(t, "postgres.query", gateway.req.Capability, "TestResource should use the Postgres query capability")
	require.JSONEq(t, `{"query":"SELECT 1","limit":1}`, string(gateway.req.Params), "TestResource should send a bounded Postgres probe")
}
