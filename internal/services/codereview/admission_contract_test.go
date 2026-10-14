package codereview

import (
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func configuredAdmissionService() *Service {
	s := &Service{}
	s.SetScheduling(db.NewCodeReviewScheduleStore(nil), &schedulingSnapshotFixture{})
	s.SetSandboxAdmission()
	return s
}

func TestSandboxAdmissionContractRequiresConfiguredGate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                           string
		scheduling, gate, missingStore bool
		want                           models.CodeReviewAdmissionContractVersion
	}{
		{name: "unconfigured"}, {name: "gate flag alone", gate: true}, {name: "scheduling alone", scheduling: true}, {name: "missing scheduling store", scheduling: true, gate: true, missingStore: true}, {name: "configured writer", scheduling: true, gate: true, want: models.CodeReviewAdmissionContractV1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Service{}
			if tt.scheduling {
				store := db.NewCodeReviewScheduleStore(nil)
				if tt.missingStore {
					store = nil
				}
				s.SetScheduling(store, &schedulingSnapshotFixture{})
			}
			if tt.gate {
				s.SetSandboxAdmission()
			}
			require.Equal(t, tt.want, s.SandboxAdmissionContractVersion(), "a compiled protocol constant must not advertise an inactive gate")
		})
	}
}

func TestAdmissionProcessEvidenceRequiresEveryWriterGraph(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, mode  string
		api, worker bool
		want        models.CodeReviewAdmissionContractVersion
	}{
		{name: "API missing gate", mode: "api"},
		{name: "API writer configured", mode: "api", api: true, want: models.CodeReviewAdmissionContractV1},
		{name: "combined API alone", mode: "all", api: true},
		{name: "combined worker alone", mode: "all", worker: true},
		{name: "combined both writers", mode: "all", api: true, worker: true, want: models.CodeReviewAdmissionContractV1},
		{name: "worker HTTP graph also required", mode: "worker", worker: true},
		{name: "worker both graphs", mode: "worker", api: true, worker: true, want: models.CodeReviewAdmissionContractV1},
		{name: "unknown mode", mode: "unknown", api: true, worker: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := NewAdmissionProcessEvidence(tt.mode, "synthetic-build")
			if tt.api {
				p.ConfigureAPI(configuredAdmissionService())
			}
			if tt.worker {
				p.ConfigureWorker(configuredAdmissionService())
			}
			e := p.Evidence()
			_, err := uuid.Parse(e.InstanceID)
			require.NoError(t, err, "instance identity must be an opaque per-process UUID")
			require.Equal(t, models.CodeReviewAdmissionProcessEvidence{InstanceID: e.InstanceID, BuildSHA: "synthetic-build", ContractVersion: tt.want}, e, "proof must describe actual writer graph readiness and this exact process build")
			require.Equal(t, e, p.Evidence(), "instance must remain stable across health and heartbeat reads")
		})
	}
}

func TestAdmissionProcessEvidenceConcurrentConfiguration(t *testing.T) {
	t.Parallel()
	p := NewAdmissionProcessEvidence("all", "synthetic-build")
	other := NewAdmissionProcessEvidence("all", "synthetic-build")
	require.NotEqual(t, p.Evidence().InstanceID, other.Evidence().InstanceID, "restarted or separate processes must not share instance evidence")
	writer := configuredAdmissionService()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { p.ConfigureAPI(writer); p.ConfigureWorker(writer); p.Evidence() })
	}
	wg.Wait()
	require.Equal(t, models.CodeReviewAdmissionContractV1, p.Evidence().ContractVersion, "concurrent startup/readout must converge on both configured graphs")
	p.ConfigureAPI(&Service{})
	require.Empty(t, p.Evidence().ContractVersion, "removing a configured writer must withdraw the positive marker")
	p.ConfigureAPI(writer)
	p.ConfigureWorker(struct{}{})
	require.Empty(t, p.Evidence().ContractVersion, "legacy lifecycle implementations must not advertise participation")
}
