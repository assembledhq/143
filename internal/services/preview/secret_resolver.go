package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type PreviewSecretBundleReader interface {
	GetActive(ctx context.Context, orgID, repositoryID uuid.UUID, name string) (*models.PreviewSecretBundle, error)
	DecryptSource(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) (models.PreviewSecretBundleSource, error)
	DecryptOutputs(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) ([]models.PreviewSecretBundleOutput, error)
}

type PreviewSecretResolver struct {
	bundles PreviewSecretBundleReader
}

func NewPreviewSecretResolver(bundles PreviewSecretBundleReader) *PreviewSecretResolver {
	return &PreviewSecretResolver{bundles: bundles}
}

func (r *PreviewSecretResolver) Resolve(ctx context.Context, orgID, repositoryID uuid.UUID, cfg *models.PreviewConfig) error {
	if cfg == nil || len(SecretBundleRefs(cfg)) == 0 {
		return nil
	}
	if repositoryID == uuid.Nil {
		return fmt.Errorf("preview secrets require a repository id")
	}
	if r == nil || r.bundles == nil {
		return fmt.Errorf("preview secret bundle resolver is not configured")
	}

	envByService := make(map[string]map[string]string)
	var files []models.PreviewRuntimeSecretFile
	for _, ref := range SecretBundleRefs(cfg) {
		row, err := r.bundles.GetActive(ctx, orgID, repositoryID, ref.Bundle)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("preview needs the %q secret bundle; ask an org admin to create it", ref.Bundle)
			}
			return fmt.Errorf("load preview secret bundle %q: %w", ref.Bundle, err)
		}
		source, err := r.bundles.DecryptSource(ctx, orgID, *row)
		if err != nil {
			return fmt.Errorf("decrypt preview secret bundle %q source: %w", ref.Bundle, err)
		}
		outputs, err := r.bundles.DecryptOutputs(ctx, orgID, *row)
		if err != nil {
			return fmt.Errorf("decrypt preview secret bundle %q outputs: %w", ref.Bundle, err)
		}
		resolvedEnv, resolvedFiles, err := renderPreviewSecretOutputs(ref, source, outputs, previewServiceNames(cfg.Services))
		if err != nil {
			return fmt.Errorf("resolve preview secret bundle %q: %w", ref.Bundle, err)
		}
		for _, service := range ref.Services {
			if _, ok := envByService[service]; !ok {
				envByService[service] = make(map[string]string)
			}
			for key, value := range resolvedEnv {
				envByService[service][key] = value
			}
		}
		files = append(files, resolvedFiles...)
	}
	cfg.RuntimeSecretEnv = envByService
	cfg.RuntimeSecretFiles = files
	return nil
}

func ValidatePreviewSecretBundle(source models.PreviewSecretBundleSource, outputs []models.PreviewSecretBundleOutput) error {
	_, _, err := renderPreviewSecretOutputs(models.PreviewSecretBundleRef{
		Bundle:   "test",
		Services: []string{"test"},
	}, source, outputs, nil)
	return err
}

func renderPreviewSecretOutputs(ref models.PreviewSecretBundleRef, source models.PreviewSecretBundleSource, outputs []models.PreviewSecretBundleOutput, allServices []string) (map[string]string, []models.PreviewRuntimeSecretFile, error) {
	if source.Type != "managed" {
		return nil, nil, fmt.Errorf("source type %q is not supported in v1", source.Type)
	}
	env := make(map[string]string)
	var files []models.PreviewRuntimeSecretFile
	for _, output := range outputs {
		switch output.Type {
		case "env":
			for key, expr := range output.Values {
				value, err := resolvePreviewSecretExpression(expr, source.Values)
				if err != nil {
					return nil, nil, fmt.Errorf("env %s: %w", key, err)
				}
				env[key] = value
			}
		case "file":
			if len(allServices) > 0 && !secretServicesCoverAll(ref.Services, allServices) {
				return nil, nil, fmt.Errorf("file outputs are workspace-wide, so services must include every preview service")
			}
			file, err := renderPreviewSecretFile(ref.Services, source.Values, output)
			if err != nil {
				return nil, nil, err
			}
			files = append(files, file)
		default:
			return nil, nil, fmt.Errorf("output type %q is not supported", output.Type)
		}
	}
	for _, required := range ref.Env {
		if _, ok := env[required]; !ok {
			return nil, nil, fmt.Errorf("required env %q is not provided by bundle outputs", required)
		}
	}
	for _, required := range ref.Files {
		if !runtimeSecretFilesContain(files, required) {
			return nil, nil, fmt.Errorf("required file %q is not provided by bundle outputs", required)
		}
	}
	return env, files, nil
}

func renderPreviewSecretFile(services []string, values map[string]string, output models.PreviewSecretBundleOutput) (models.PreviewRuntimeSecretFile, error) {
	if errs := validateSecretFilePath("secret output file", output.Path); len(errs) > 0 {
		return models.PreviewRuntimeSecretFile{}, errors.New(strings.Join(errs, "; "))
	}
	format := output.Format
	if format == "" {
		format = "raw"
	}
	var content []byte
	var err error
	switch format {
	case "json":
		var v any
		if len(output.Content) == 0 {
			v = map[string]any{}
		} else if err := json.Unmarshal(output.Content, &v); err != nil {
			return models.PreviewRuntimeSecretFile{}, fmt.Errorf("parse json file content: %w", err)
		}
		resolved, err := resolvePreviewSecretJSON(v, values)
		if err != nil {
			return models.PreviewRuntimeSecretFile{}, err
		}
		content, err = json.MarshalIndent(resolved, "", "  ")
		if err != nil {
			return models.PreviewRuntimeSecretFile{}, err
		}
		content = append(content, '\n')
	case "env":
		content, err = renderDotenv(output.Values, values)
	case "raw":
		contentValue := output.Value
		if contentValue == "" && len(output.Content) > 0 {
			if err := json.Unmarshal(output.Content, &contentValue); err != nil {
				return models.PreviewRuntimeSecretFile{}, fmt.Errorf("raw file content must be a string")
			}
		}
		resolved, err := resolvePreviewSecretExpression(contentValue, values)
		if err != nil {
			return models.PreviewRuntimeSecretFile{}, err
		}
		content = []byte(resolved)
	default:
		return models.PreviewRuntimeSecretFile{}, fmt.Errorf("file format %q is not supported", format)
	}
	if err != nil {
		return models.PreviewRuntimeSecretFile{}, err
	}
	mode := output.Mode
	if mode == "" {
		mode = "0600"
	}
	return models.PreviewRuntimeSecretFile{
		Services: append([]string(nil), services...),
		Path:     filepath.Clean(output.Path),
		Mode:     mode,
		Content:  content,
	}, nil
}

func resolvePreviewSecretExpression(expr string, values map[string]string) (string, error) {
	switch {
	case strings.HasPrefix(expr, "secret:"):
		key := strings.TrimPrefix(expr, "secret:")
		value, ok := values[key]
		if !ok {
			return "", fmt.Errorf("secret %q is missing", key)
		}
		return value, nil
	case strings.HasPrefix(expr, "literal:"):
		return strings.TrimPrefix(expr, "literal:"), nil
	default:
		return expr, nil
	}
}

func resolvePreviewSecretJSON(v any, values map[string]string) (any, error) {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			resolved, err := resolvePreviewSecretJSON(value, values)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			resolved, err := resolvePreviewSecretJSON(value, values)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	case string:
		return resolvePreviewSecretExpression(typed, values)
	default:
		return typed, nil
	}
}

func renderDotenv(expressions map[string]string, values map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(expressions))
	for key := range expressions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, key := range keys {
		value, err := resolvePreviewSecretExpression(expressions[key], values)
		if err != nil {
			return nil, fmt.Errorf("env file key %s: %w", key, err)
		}
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(strconv.Quote(value))
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func runtimeSecretFilesContain(files []models.PreviewRuntimeSecretFile, path string) bool {
	clean := filepath.Clean(path)
	for _, file := range files {
		if file.Path == clean {
			return true
		}
	}
	return false
}
