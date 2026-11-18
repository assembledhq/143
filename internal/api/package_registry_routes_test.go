package api

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageRegistryCredentialRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("router.go")
	require.NoError(t, err, "router source should be readable")
	routes := []string{`r.Get("/api/v1/repositories/{id}/package-registry-credentials"`, `r.Post("/api/v1/repositories/{id}/package-registry-credentials"`, `r.Put("/api/v1/repositories/{id}/package-registry-credentials/{credential_id}"`, `r.Delete("/api/v1/repositories/{id}/package-registry-credentials/{credential_id}"`}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			index := strings.Index(string(source), route)
			require.Greater(t, index, 0, "package registry route must be mounted")
			before := string(source[:index])
			group := strings.LastIndex(before, "r.Use(middleware.RequireRole(")
			require.Greater(t, group, 0, "route should have a role guard")
			require.True(t, strings.HasPrefix(before[group:], `r.Use(middleware.RequireRole("admin"))`), "metadata reads and mutations must live inside the admin-only route group")
		})
	}
}
