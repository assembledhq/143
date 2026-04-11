package providers

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/preview"
	"github.com/stretchr/testify/require"
)

func TestBuildInfraEnv_Postgres(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{
		Username: "preview_db",
		Password: "secret123",
		Database: "preview_db",
	}
	env := d.buildInfraEnv("postgres-17", cred)
	require.Contains(t, env, "POSTGRES_USER=preview_db")
	require.Contains(t, env, "POSTGRES_PASSWORD=secret123")
	require.Contains(t, env, "POSTGRES_DB=preview_db")
}

func TestBuildInfraEnv_Redis(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{Password: "redispass"}
	env := d.buildInfraEnv("redis-7", cred)
	require.Equal(t, []string{"REDIS_PASSWORD=redispass"}, env)
}

func TestBuildInfraEnv_MySQL(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	cred := preview.InfraCredential{
		Username: "preview_db",
		Password: "mysqlpass",
		Database: "preview_db",
	}
	env := d.buildInfraEnv("mysql-8", cred)
	require.Len(t, env, 4)
	require.Contains(t, env, "MYSQL_ROOT_PASSWORD=mysqlpass")
	require.Contains(t, env, "MYSQL_USER=preview_db")
	require.Contains(t, env, "MYSQL_PASSWORD=mysqlpass")
	require.Contains(t, env, "MYSQL_DATABASE=preview_db")
}

func TestBuildInfraEnv_Unknown(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}
	env := d.buildInfraEnv("unknown-1", preview.InfraCredential{})
	require.Nil(t, env)
}

func TestResolveCredentialTemplate(t *testing.T) {
	t.Parallel()
	cred := preview.InfraCredential{
		Host:     "preview-db-abc123",
		Port:     5432,
		Username: "preview_db",
		Password: "secret",
		Database: "preview_db",
	}
	result := resolveCredentialTemplate("postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}", cred)
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc123:5432/preview_db", result)
}

func TestGenerateInfraCredential(t *testing.T) {
	t.Parallel()
	cred, err := generateInfraCredential("db")
	require.NoError(t, err)
	require.Equal(t, "preview_db", cred.Username)
	require.Equal(t, "preview_db", cred.Database)
	require.Len(t, cred.Password, 32) // 16 bytes → 32 hex chars
}

func TestGenerateHandle(t *testing.T) {
	t.Parallel()
	h1, err := preview.RandomHex(16)
	require.NoError(t, err)
	h2, err := preview.RandomHex(16)
	require.NoError(t, err)
	require.Len(t, h1, 32)
	require.NotEqual(t, h1, h2)
}

func TestShellEscape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, shellEscape(tt.input))
		})
	}
}

func TestBuildServiceEnvs(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Port: 3000,
				Env:  map[string]string{"NODE_ENV": "development"},
			},
			"worker": {
				Port: 4000,
				Env:  map[string]string{"WORKER_THREADS": "2"},
			},
		},
		Infrastructure: map[string]models.InfrastructureConfig{
			"db": {
				Template:  "postgres-17",
				InjectEnv: map[string]string{"DATABASE_URL": "postgres://{{username}}:{{password}}@{{host}}:{{port}}/{{database}}"},
				InjectInto: []string{"web", "worker"},
			},
		},
	}

	infraCreds := map[string]preview.InfraCredential{
		"db": {
			Host:     "preview-db-abc",
			Port:     5432,
			Username: "preview_db",
			Password: "secret",
			Database: "preview_db",
		},
	}

	envs := d.buildServiceEnvs(cfg, infraCreds)

	// web should have NODE_ENV + DATABASE_URL
	require.Equal(t, "development", envs["web"]["NODE_ENV"])
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc:5432/preview_db", envs["web"]["DATABASE_URL"])

	// worker should have WORKER_THREADS + DATABASE_URL
	require.Equal(t, "2", envs["worker"]["WORKER_THREADS"])
	require.Equal(t, "postgres://preview_db:secret@preview-db-abc:5432/preview_db", envs["worker"]["DATABASE_URL"])
}

func TestBuildServiceEnvs_NoInfra(t *testing.T) {
	t.Parallel()
	d := &DockerPreviewProvider{}

	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Port: 3000,
				Env:  map[string]string{"PORT": "3000"},
			},
		},
		Infrastructure: map[string]models.InfrastructureConfig{},
	}

	envs := d.buildServiceEnvs(cfg, nil)
	require.Equal(t, "3000", envs["web"]["PORT"])
	require.Len(t, envs["web"], 1)
}

func TestTcpPreviewStream_NilClose(t *testing.T) {
	t.Parallel()
	s := &tcpPreviewStream{Conn: nil}
	require.NoError(t, s.Close())
}
