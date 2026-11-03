package packagecredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrEncryptionUnavailable = errors.New("package registry credential encryption is not configured")
var ErrInvalidCredential = errors.New("invalid package registry credential")
var ErrCredentialBinding = errors.New("package registry credential identity mismatch")

var npmScope = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]{0,213}$`)
var registryHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

type Store interface {
	RepositoryExists(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	ListMetadata(context.Context, uuid.UUID, uuid.UUID) ([]models.PackageRegistryCredentialSummary, error)
	GetMetadata(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.PackageRegistryCredentialSummary, error)
	Create(context.Context, uuid.UUID, models.PackageRegistryCredential) (models.PackageRegistryCredentialSummary, error)
	Rotate(context.Context, uuid.UUID, models.PackageRegistryCredential, uuid.UUID) (models.PackageRegistryCredentialSummary, error)
	Delete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error
	ListEncrypted(context.Context, uuid.UUID, uuid.UUID) ([]models.PackageRegistryCredential, error)
}

type Service struct {
	store  Store
	crypto *crypto.Service
}

func NewService(store Store, cryptoSvc *crypto.Service) *Service {
	return &Service{store: store, crypto: cryptoSvc}
}

type encryptedCredential struct {
	ID           uuid.UUID `json:"id"`
	Revision     uuid.UUID `json:"revision"`
	OrgID        uuid.UUID `json:"org_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	RegistryURL  string    `json:"registry_url"`
	PackageScope string    `json:"package_scope"`
	Token        string    `json:"token"`
}

func (s *Service) Metadata(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	if err := s.repository(ctx, orgID, repositoryID); err != nil {
		return nil, err
	}
	rows, err := s.store.ListMetadata(ctx, orgID, repositoryID)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []models.PackageRegistryCredentialSummary{}
	}
	return rows, nil
}

func (s *Service) Create(ctx context.Context, orgID, repositoryID uuid.UUID, registryURL, scope, token string) (models.PackageRegistryCredentialSummary, error) {
	canonical, err := ValidateRegistry(registryURL, scope)
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	if err := validateToken(token); err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	if err := s.repository(ctx, orgID, repositoryID); err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	row := models.PackageRegistryCredential{PackageRegistryCredentialSummary: models.PackageRegistryCredentialSummary{ID: uuid.New(), RepositoryID: repositoryID, RegistryURL: canonical, PackageScope: scope, Revision: uuid.New()}, OrgID: orgID}
	row.TokenEncrypted, err = s.encrypt(row, token)
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	return s.store.Create(ctx, orgID, row)
}

func (s *Service) Rotate(ctx context.Context, orgID, repositoryID, id, expectedRevision uuid.UUID, token string) (models.PackageRegistryCredentialSummary, error) {
	if err := validateToken(token); err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	metadata, err := s.store.GetMetadata(ctx, orgID, repositoryID, id)
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	if expectedRevision == uuid.Nil || metadata.Revision != expectedRevision {
		return models.PackageRegistryCredentialSummary{}, db.ErrPackageRegistryCredentialConflict
	}
	metadata.Revision = uuid.New()
	row := models.PackageRegistryCredential{PackageRegistryCredentialSummary: metadata, OrgID: orgID}
	row.TokenEncrypted, err = s.encrypt(row, token)
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	return s.store.Rotate(ctx, orgID, row, expectedRevision)
}

func (s *Service) Delete(ctx context.Context, orgID, repositoryID, id, expectedRevision uuid.UUID) error {
	metadata, err := s.store.GetMetadata(ctx, orgID, repositoryID, id)
	if err != nil {
		return err
	}
	if expectedRevision == uuid.Nil || metadata.Revision != expectedRevision {
		return db.ErrPackageRegistryCredentialConflict
	}
	return s.store.Delete(ctx, orgID, repositoryID, id, expectedRevision)
}

func (s *Service) Resolve(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.ResolvedPackageRegistryCredential, error) {
	if orgID == uuid.Nil || repositoryID == uuid.Nil {
		return nil, pgx.ErrNoRows
	}
	rows, err := s.store.ListEncrypted(ctx, orgID, repositoryID)
	if err != nil {
		return nil, err
	}
	result := make([]models.ResolvedPackageRegistryCredential, 0, len(rows))
	for _, row := range rows {
		if s.crypto == nil {
			return nil, ErrEncryptionUnavailable
		}
		if row.OrgID != orgID || row.RepositoryID != repositoryID {
			return nil, ErrCredentialBinding
		}
		plaintext, err := s.crypto.Decrypt(row.TokenEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt package registry credential: %w", err)
		}
		var secret encryptedCredential
		err = json.Unmarshal(plaintext, &secret)
		clear(plaintext)
		if err != nil {
			return nil, errors.New("invalid encrypted package registry credential")
		}
		if secret.ID != row.ID || secret.Revision != row.Revision || secret.OrgID != orgID || secret.RepositoryID != repositoryID || secret.RegistryURL != row.RegistryURL || secret.PackageScope != row.PackageScope {
			return nil, ErrCredentialBinding
		}
		canonical, err := ValidateRegistry(row.RegistryURL, row.PackageScope)
		if err != nil || canonical != row.RegistryURL {
			return nil, ErrCredentialBinding
		}
		if err := validateToken(secret.Token); err != nil {
			return nil, ErrCredentialBinding
		}
		result = append(result, models.ResolvedPackageRegistryCredential{Metadata: row.PackageRegistryCredentialSummary, Token: secret.Token})
	}
	return result, nil
}

func (s *Service) repository(ctx context.Context, orgID, repositoryID uuid.UUID) error {
	if orgID == uuid.Nil || repositoryID == uuid.Nil {
		return pgx.ErrNoRows
	}
	exists, err := s.store.RepositoryExists(ctx, orgID, repositoryID)
	if err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Service) encrypt(row models.PackageRegistryCredential, token string) ([]byte, error) {
	if s.crypto == nil {
		return nil, ErrEncryptionUnavailable
	}
	plaintext, err := json.Marshal(encryptedCredential{ID: row.ID, Revision: row.Revision, OrgID: row.OrgID, RepositoryID: row.RepositoryID, RegistryURL: row.RegistryURL, PackageScope: row.PackageScope, Token: token})
	if err != nil {
		return nil, errors.New("encode package registry credential")
	}
	defer clear(plaintext)
	return s.crypto.Encrypt(plaintext)
}

func ValidateRegistry(rawURL, scope string) (string, error) {
	if !npmScope.MatchString(scope) {
		return "", fmt.Errorf("%w: package_scope must be a lowercase @npm scope", ErrInvalidCredential)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Port() != "" && parsed.Port() != "443") {
		return "", fmt.Errorf("%w: registry_url must be an HTTPS origin", ErrInvalidCredential)
	}
	host := strings.ToLower(parsed.Hostname())
	if len(host) > 253 || !registryHost.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") || net.ParseIP(host) != nil {
		return "", fmt.Errorf("%w: registry_url must use a DNS hostname", ErrInvalidCredential)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("%w: registry_url must use a DNS hostname", ErrInvalidCredential)
		}
	}
	return "https://" + host + "/", nil
}

func validateToken(token string) error {
	if token == "" || len(token) > 8192 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%w: token must be nonempty and contain no whitespace or control characters", ErrInvalidCredential)
	}
	return nil
}
