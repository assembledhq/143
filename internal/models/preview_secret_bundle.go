package models

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	previewSecretBundleNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	previewSecretEnvNamePattern    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// PreviewSecretBundle is the decrypted form of an admin-managed preview env
// bundle. Env values are secrets and must never be returned in list responses.
type PreviewSecretBundle struct {
	ID        uuid.UUID         `json:"id"`
	OrgID     uuid.UUID         `json:"org_id"`
	Name      string            `json:"name"`
	Env       map[string]string `json:"-"`
	CreatedBy *uuid.UUID        `json:"created_by,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// PreviewSecretBundleSummary is safe to return to the UI.
type PreviewSecretBundleSummary struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	EnvNames  []string   `json:"env_names"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// PreviewSecretBundleInput is used for create/update requests.
type PreviewSecretBundleInput struct {
	Name string            `json:"name"`
	Env  map[string]string `json:"env"`
}

func (i PreviewSecretBundleInput) Validate() error {
	name := strings.TrimSpace(i.Name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !previewSecretBundleNamePattern.MatchString(name) {
		return fmt.Errorf("name must contain only letters, numbers, dots, underscores, and hyphens")
	}
	if len(i.Env) == 0 {
		return fmt.Errorf("env must include at least one variable")
	}
	for key := range i.Env {
		if !previewSecretEnvNamePattern.MatchString(key) {
			return fmt.Errorf("env var %q must match [A-Z_][A-Z0-9_]*", key)
		}
	}
	return nil
}

func (i PreviewSecretBundleInput) Normalized() PreviewSecretBundleInput {
	env := make(map[string]string, len(i.Env))
	for key, value := range i.Env {
		env[strings.TrimSpace(key)] = value
	}
	return PreviewSecretBundleInput{
		Name: strings.TrimSpace(i.Name),
		Env:  env,
	}
}

func PreviewSecretEnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for key := range env {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}
