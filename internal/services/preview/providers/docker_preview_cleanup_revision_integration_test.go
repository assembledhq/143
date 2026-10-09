//go:build docker_integration

package providers

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/preview"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDockerIntegrationPreviewInfrastructureReconcilesCurrentWorkerAfterStartup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		starting        bool
		acknowledged    bool
		cleanupEligible bool
	}{
		{name: "unacknowledged handle remains protected", cleanupEligible: true},
		{name: "in-flight start remains protected", starting: true, cleanupEligible: true},
		{name: "durable active handle remains protected", acknowledged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newDockerCleanupIntegrationFixture(t)
			created := time.Now().Add(-infrastructureCleanupGrace - time.Minute)
			fixture.provider.cleanupStartupCutoff = created.Add(-time.Hour)
			require.True(t, created.After(fixture.provider.cleanupStartupCutoff), "the aged resources must have been labelled after this provider started")

			orphanID, orphanVolume := createRevisionOwnedPostgresSleeper(t, fixture, "current-worker-orphan", fixture.owner, created)
			protectedOwner := fixture.owner
			protectedOwner.PreviewID = uuid.New()
			protectedOwner.Handle = uuid.NewString()
			protectedID, protectedVolume := createRevisionOwnedPostgresSleeper(t, fixture, "current-worker-protected", protectedOwner, created)
			protectedOpts := fixture.opts
			protectedOpts.PreviewID = protectedOwner.PreviewID
			fixture.provider.previews[protectedOwner.Handle] = &previewState{
				handle: protectedOwner.Handle, starting: tt.starting, acknowledged: tt.acknowledged,
				sandbox: &agent.Sandbox{ID: protectedID}, opts: protectedOpts,
				infra: make(map[string]*preview.InfraHandle), cancelFn: func() {},
			}
			fixture.provider.cleanupResolver = func(_ context.Context, owners []preview.InfrastructureOwner) (map[preview.InfrastructureOwner]bool, error) {
				eligible := make(map[preview.InfrastructureOwner]bool, len(owners))
				for _, owner := range owners {
					eligible[owner] = owner == fixture.owner || (owner == protectedOwner && tt.cleanupEligible)
				}
				return eligible, nil
			}

			// Parallel fixtures share the Docker inventory, so a bounded batch
			// can select a different fixture first. Keep this same provider
			// alive while its cursors rotate through those candidates.
			for pass := 0; pass < 32; pass++ {
				require.NoError(t, fixture.provider.ReconcileInfrastructure(fixture.ctx), "the existing provider should reconcile an aged current-worker orphan without a restart")
				_, containerErr := fixture.client.ContainerInspect(fixture.ctx, orphanID)
				_, volumeErr := fixture.client.VolumeInspect(fixture.ctx, orphanVolume.Name)
				if cerrdefs.IsNotFound(containerErr) && cerrdefs.IsNotFound(volumeErr) {
					break
				}
				if !cerrdefs.IsNotFound(containerErr) {
					require.NoError(t, containerErr, "checking reconciliation progress should inspect the exact orphan container")
				}
				if !cerrdefs.IsNotFound(volumeErr) {
					require.NoError(t, volumeErr, "checking reconciliation progress should inspect the exact orphan volume")
				}
			}
			_, err := fixture.client.ContainerInspect(fixture.ctx, orphanID)
			require.True(t, cerrdefs.IsNotFound(err), "reconciliation must remove the exact current-worker orphan container labelled after startup")
			_, err = fixture.client.VolumeInspect(fixture.ctx, orphanVolume.Name)
			require.True(t, cerrdefs.IsNotFound(err), "reconciliation must remove the orphan container's exact labelled anonymous data volume")

			remainingContainer, err := fixture.client.ContainerInspect(fixture.ctx, protectedID)
			require.NoError(t, err, "reconciliation must preserve the same-worker protected companion container")
			require.Equal(t, protectedID, remainingContainer.ID, "the protected companion must retain its exact container identity")
			require.True(t, remainingContainer.State.Running, "the protected companion must remain running after orphan reconciliation")
			remainingOwner, valid := parseInfrastructureOwner(remainingContainer.Config.Labels)
			require.True(t, valid, "the protected companion must retain its complete versioned ownership labels")
			require.Equal(t, protectedOwner, remainingOwner, "reconciliation must preserve the protected companion's full ownership tuple")
			remainingVolume, err := fixture.client.VolumeInspect(fixture.ctx, protectedVolume.Name)
			require.NoError(t, err, "reconciliation must preserve the protected companion's anonymous data volume")
			require.Equal(t, integrationVolumeIdentity(protectedVolume), integrationVolumeIdentity(remainingVolume), "reconciliation must preserve the protected volume's exact identity and labels")
		})
	}
}

func createRevisionOwnedPostgresSleeper(t *testing.T, fixture *dockerCleanupIntegrationFixture, suffix string, owner preview.InfrastructureOwner, created time.Time) (string, volume.Volume) {
	t.Helper()
	labels := infrastructureLabels(owner, "postgres", created)
	volumeLabels := infrastructureLabels(owner, "postgres", created)
	volumeLabels[infrastructureVolumeKindLabel] = "anonymous"
	id := fixture.createContainer(t, suffix, &container.Config{
		Image: fixture.template.Image, Entrypoint: []string{"sleep"}, Cmd: []string{"300"}, Labels: labels,
	}, []mount.Mount{{Type: mount.TypeVolume, Target: "/var/lib/postgresql/data", VolumeOptions: &mount.VolumeOptions{Labels: volumeLabels}}})
	inspected, err := fixture.client.ContainerInspect(fixture.ctx, id)
	require.NoError(t, err, "the owned PostgreSQL image fixture should expose its actual anonymous data mount")
	mountTypes := make(map[string]mount.Type)
	volumeName := ""
	for _, point := range inspected.Mounts {
		mountTypes[point.Destination] = point.Type
		if point.Destination == "/var/lib/postgresql/data" {
			volumeName = point.Name
		}
	}
	require.Equal(t, map[string]mount.Type{"/var/lib/postgresql/data": mount.TypeVolume}, mountTypes, "the fixture should own exactly the PostgreSQL image's declared data mount")
	fixture.cleanupVolume(t, volumeName)
	v, err := fixture.client.VolumeInspect(fixture.ctx, volumeName)
	require.NoError(t, err, "the real anonymous data volume should exist before reconciliation")
	actualOwner, valid := parseInfrastructureOwner(v.Labels)
	require.True(t, valid, "the anonymous data volume should contain complete recoverable ownership labels")
	require.Equal(t, owner, actualOwner, "the fixture data volume should belong to the exact container owner")
	require.True(t, managedAnonymousVolume(v), "the fixture's real anonymous data volume should qualify for scoped cleanup")
	return id, v
}
