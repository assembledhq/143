package preview

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
)

var (
	// ErrNoPreviewWorkers reports that no active preview-capable worker is available.
	ErrNoPreviewWorkers = errors.New("no active preview-capable worker available")
	// ErrLegacySessionWorkerOwnership reports that a live session predates
	// worker ownership tracking, so preview reuse cannot be routed safely.
	ErrLegacySessionWorkerOwnership = errors.New("live session is missing worker ownership metadata")
	// ErrWorkerNodeNotRoutable reports that a known worker node cannot receive
	// preview traffic because its cluster status is not active/draining.
	ErrWorkerNodeNotRoutable = errors.New("worker node is not routable")
)

// LiveSessionWorkerOwnerNotRoutableError reports that a session's recorded
// live sandbox owner is no longer routable. Active previews must stay pinned
// to their runtime owner, but live-session reuse can recover by clearing the
// stale session container ownership and hydrating from a snapshot.
type LiveSessionWorkerOwnerNotRoutableError struct {
	WorkerNodeID string
	ContainerID  string
	Err          error
}

func (e *LiveSessionWorkerOwnerNotRoutableError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("live session worker owner %s is not routable: %s", e.WorkerNodeID, e.Err.Error())
	}
	return fmt.Sprintf("live session worker owner %s is not routable", e.WorkerNodeID)
}

func (e *LiveSessionWorkerOwnerNotRoutableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

const (
	// rendezvousTopN is the number of top-scored rendezvous candidates to check
	// for capacity before falling through to the least-loaded fallback.
	rendezvousTopN = 6

	dependencyCacheLookupTimeout = 250 * time.Millisecond
)

// WorkerNodeMetadata is the routable subset of nodes.metadata used by preview.
type WorkerNodeMetadata struct {
	BuildSHA               string `json:"build_sha,omitempty"`
	Region                 string `json:"region,omitempty"`
	PreviewCapable         bool   `json:"preview_capable,omitempty"`
	PreviewRPCAuthCheck    bool   `json:"preview_rpc_auth_check,omitempty"`
	PreviewInternalBaseURL string `json:"preview_internal_base_url,omitempty"`
	StaticEgressCapable    bool   `json:"static_egress_capable,omitempty"`
	StaticEgressPublicIP   string `json:"static_egress_public_ip,omitempty"`
}

// WorkerNode is a preview-routable worker node.
type WorkerNode struct {
	ID      string
	Mode    string
	BaseURL string
	Region  string
}

// WorkerSelectionRequirements constrains cold-start worker selection.
type WorkerSelectionRequirements struct {
	StaticEgressRequired bool
	StaticEgressPublicIP string
}

type WorkerCachePlacement struct {
	Kind         models.PreviewCacheKind
	PlacementKey string
	Approximate  bool
}

type StaticEgressWorkerDiagnostics struct {
	Available  bool                         `json:"available"`
	Mismatches []StaticEgressWorkerMismatch `json:"mismatches,omitempty"`
}

type StaticEgressWorkerMismatch struct {
	NodeID               string `json:"node_id,omitempty"`
	Host                 string `json:"host,omitempty"`
	Mode                 string `json:"mode,omitempty"`
	StaticEgressCapable  bool   `json:"static_egress_capable"`
	StaticEgressPublicIP string `json:"static_egress_public_ip,omitempty"`
	Reason               string `json:"reason"`
}

// WorkerSelector resolves preview-owning workers and selects workers for cold starts.
type WorkerSelector struct {
	nodes                *db.NodeStore
	previews             *db.PreviewStore
	maxPreviewsPerWorker int
	preferredRegion      string
	cacheLookupTimeout   time.Duration
}

type WorkerSelectorOptions struct {
	MaxPreviewsPerWorker int
	PreferredRegion      string
}

// NewWorkerSelector creates a new worker selector.
func NewWorkerSelector(nodes *db.NodeStore, previews *db.PreviewStore) *WorkerSelector {
	return NewWorkerSelectorWithMaxPerWorker(nodes, previews, DefaultMaxPreviewsPerWorker)
}

func NewWorkerSelectorWithMaxPerWorker(nodes *db.NodeStore, previews *db.PreviewStore, maxPreviewsPerWorker int) *WorkerSelector {
	return NewWorkerSelectorWithOptions(nodes, previews, WorkerSelectorOptions{MaxPreviewsPerWorker: maxPreviewsPerWorker})
}

func NewWorkerSelectorWithOptions(nodes *db.NodeStore, previews *db.PreviewStore, opts WorkerSelectorOptions) *WorkerSelector {
	maxPreviewsPerWorker := opts.MaxPreviewsPerWorker
	if maxPreviewsPerWorker <= 0 {
		maxPreviewsPerWorker = DefaultMaxPreviewsPerWorker
	}
	return &WorkerSelector{
		nodes:                nodes,
		previews:             previews,
		maxPreviewsPerWorker: maxPreviewsPerWorker,
		preferredRegion:      strings.TrimSpace(opts.PreferredRegion),
		cacheLookupTimeout:   dependencyCacheLookupTimeout,
	}
}

func parseWorkerNodeMetadata(node models.Node) (WorkerNodeMetadata, error) {
	var metadata WorkerNodeMetadata
	if len(node.Metadata) > 0 {
		if err := json.Unmarshal(node.Metadata, &metadata); err != nil {
			return WorkerNodeMetadata{}, fmt.Errorf("parse node metadata: %w", err)
		}
	}
	return metadata, nil
}

func parseWorkerNodeFromMetadata(node models.Node, metadata WorkerNodeMetadata) (WorkerNode, error) {
	if !metadata.PreviewCapable {
		return WorkerNode{}, fmt.Errorf("node %s is not preview-capable", node.ID)
	}
	if !metadata.PreviewRPCAuthCheck {
		return WorkerNode{}, fmt.Errorf("node %s preview RPC endpoint is not verified", node.ID)
	}
	baseURL := strings.TrimRight(metadata.PreviewInternalBaseURL, "/")
	if baseURL == "" {
		return WorkerNode{}, fmt.Errorf("node %s has no preview internal base url", node.ID)
	}
	return WorkerNode{
		ID:      node.ID,
		Mode:    string(node.Mode),
		BaseURL: baseURL,
		Region:  strings.TrimSpace(metadata.Region),
	}, nil
}

func parseWorkerNode(node models.Node) (WorkerNode, error) {
	metadata, err := parseWorkerNodeMetadata(node)
	if err != nil {
		return WorkerNode{}, err
	}
	return parseWorkerNodeFromMetadata(node, metadata)
}

func parseWorkerNodeWithRequirements(node models.Node, req WorkerSelectionRequirements) (WorkerNode, error) {
	metadata, err := parseWorkerNodeMetadata(node)
	if err != nil {
		return WorkerNode{}, err
	}
	if req.StaticEgressRequired {
		if req.StaticEgressPublicIP == "" {
			return WorkerNode{}, fmt.Errorf("static egress public IP is required")
		}
		if !workerMetadataMatchesStaticEgress(metadata, req.StaticEgressPublicIP) {
			return WorkerNode{}, fmt.Errorf("node %s is not static-egress capable", node.ID)
		}
	}
	return parseWorkerNodeFromMetadata(node, metadata)
}

func parseRoutableWorkerNode(node models.Node) (WorkerNode, error) {
	var metadata WorkerNodeMetadata
	if len(node.Metadata) > 0 {
		if err := json.Unmarshal(node.Metadata, &metadata); err != nil {
			return WorkerNode{}, fmt.Errorf("parse node metadata: %w", err)
		}
	}
	baseURL := strings.TrimRight(metadata.PreviewInternalBaseURL, "/")
	if baseURL == "" {
		return WorkerNode{}, fmt.Errorf("node %s has no preview internal base url", node.ID)
	}
	return WorkerNode{
		ID:      node.ID,
		Mode:    string(node.Mode),
		BaseURL: baseURL,
		Region:  strings.TrimSpace(metadata.Region),
	}, nil
}

func isResolvableNodeStatus(status models.NodeStatus) bool {
	return status == models.NodeStatusActive || status == models.NodeStatusDraining
}

func nodeCanClaimSessionJobs(node models.Node) bool {
	mode := string(node.Mode)
	return mode == "worker" || mode == "all"
}

func workerMetadataMatchesStaticEgress(metadata WorkerNodeMetadata, publicIP string) bool {
	return publicIP != "" && metadata.StaticEgressCapable && metadata.StaticEgressPublicIP == publicIP
}

func staticEgressMismatchReason(metadata WorkerNodeMetadata, publicIP string) string {
	if !metadata.StaticEgressCapable {
		return "missing static egress capability"
	}
	if metadata.StaticEgressPublicIP != publicIP {
		return "static egress public IP mismatch"
	}
	return "public IP not configured"
}

// ResolveNode returns a routable worker by ID. Existing previews and live
// sandboxes stay pinned to their owning worker, so routing only requires the
// internal base URL; cold-start selection still requires preview_capable.
func (s *WorkerSelector) ResolveNode(ctx context.Context, nodeID string) (WorkerNode, error) {
	node, err := s.nodes.GetByID(ctx, nodeID)
	if err != nil {
		return WorkerNode{}, err
	}
	if !isResolvableNodeStatus(node.Status) {
		return WorkerNode{}, fmt.Errorf("node %s is not routable: %w", nodeID, ErrWorkerNodeNotRoutable)
	}
	return parseRoutableWorkerNode(*node)
}

// ResolveNodeWithRequirements returns a routable worker by ID only if its
// metadata satisfies current cold-start requirements.
func (s *WorkerSelector) ResolveNodeWithRequirements(ctx context.Context, nodeID string, req WorkerSelectionRequirements) (WorkerNode, error) {
	node, err := s.nodes.GetByID(ctx, nodeID)
	if err != nil {
		return WorkerNode{}, err
	}
	if !isResolvableNodeStatus(node.Status) {
		return WorkerNode{}, fmt.Errorf("node %s is not routable: %w", nodeID, ErrWorkerNodeNotRoutable)
	}
	return parseWorkerNodeWithRequirements(*node, req)
}

// SelectStartNode picks the worker that should handle Start Preview for the session.
func (s *WorkerSelector) SelectStartNode(ctx context.Context, orgID uuid.UUID, session *models.Session) (WorkerNode, error) {
	return s.SelectStartNodeWithRequirements(ctx, orgID, session, WorkerSelectionRequirements{})
}

// SelectStartNodeWithRequirements picks the worker that should handle Start
// Preview for the session while honoring optional runtime capabilities.
func (s *WorkerSelector) SelectStartNodeWithRequirements(ctx context.Context, orgID uuid.UUID, session *models.Session, req WorkerSelectionRequirements) (WorkerNode, error) {
	return s.SelectStartNodeWithPlacementAndRequirements(ctx, orgID, session, uuid.Nil, "", req)
}

func (s *WorkerSelector) SelectStartNodeWithPlacement(ctx context.Context, orgID uuid.UUID, session *models.Session, repoID uuid.UUID, placementKey string) (WorkerNode, error) {
	return s.SelectStartNodeWithPlacementAndRequirements(ctx, orgID, session, repoID, placementKey, WorkerSelectionRequirements{})
}

func (s *WorkerSelector) SelectStartNodeWithPlacementAndRequirements(ctx context.Context, orgID uuid.UUID, session *models.Session, repoID uuid.UUID, placementKey string, req WorkerSelectionRequirements) (WorkerNode, error) {
	var placements []WorkerCachePlacement
	if strings.TrimSpace(placementKey) != "" {
		placements = []WorkerCachePlacement{{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: placementKey}}
	}
	return s.SelectStartNodeWithCachePlacementsAndRequirements(ctx, orgID, session, repoID, placements, req)
}

func (s *WorkerSelector) SelectStartNodeWithCachePlacementsAndRequirements(ctx context.Context, orgID uuid.UUID, session *models.Session, repoID uuid.UUID, placements []WorkerCachePlacement, req WorkerSelectionRequirements) (WorkerNode, error) {
	if session == nil {
		return WorkerNode{}, fmt.Errorf("session is required")
	}

	instance, err := s.previews.GetActivePreviewForSession(ctx, orgID, session.ID)
	if err == nil && instance != nil {
		metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "live_session")
		return s.ResolveNode(ctx, instance.WorkerNodeID)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return WorkerNode{}, fmt.Errorf("lookup active preview: %w", err)
	}

	if session.ContainerID != nil && *session.ContainerID != "" &&
		session.SandboxState == models.SandboxStateRunning {
		if session.WorkerNodeID == nil || *session.WorkerNodeID == "" {
			return WorkerNode{}, ErrLegacySessionWorkerOwnership
		}
		metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "live_session")
		worker, err := s.ResolveNode(ctx, *session.WorkerNodeID)
		if err != nil {
			if errors.Is(err, ErrWorkerNodeNotRoutable) {
				return WorkerNode{}, &LiveSessionWorkerOwnerNotRoutableError{
					WorkerNodeID: *session.WorkerNodeID,
					ContainerID:  *session.ContainerID,
					Err:          err,
				}
			}
			return WorkerNode{}, err
		}
		return worker, nil
	}

	placements = normalizeWorkerCachePlacements(placements)
	if repoID != uuid.Nil && len(placements) > 0 {
		worker, ok, lookupErr := s.selectCachePlacementWorker(ctx, orgID, repoID, placements, true, req)
		if lookupErr == nil && ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "local_cache_holder")
			return worker, nil
		} else if lookupErr != nil {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "fallback_error")
		}
		if workerCachePlacementsApproximate(placements) {
			if worker, ok, err := s.selectRecentRepoCacheWorker(ctx, orgID, repoID, true, req); err != nil {
				metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "fallback_error")
			} else if ok {
				metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "repo_cache_holder")
				return worker, nil
			}
		}
		if worker, ok, err := s.selectRendezvousWorker(ctx, orgID, repoID, rendezvousPlacementKey(placements), rendezvousTopN, true, req); err != nil && !errors.Is(err, ErrNoPreviewWorkers) {
			return WorkerNode{}, err
		} else if ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "rendezvous")
			return worker, nil
		}
	}
	if repoID != uuid.Nil && len(placements) == 0 {
		if worker, ok, err := s.selectRecentRepoCacheWorker(ctx, orgID, repoID, true, req); err != nil {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "fallback_error")
		} else if ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "repo_cache_holder")
			return worker, nil
		}
	}

	worker, err := s.selectLeastLoadedNode(ctx, nil, true, req)
	if err == nil {
		metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "least_loaded")
		return worker, nil
	}
	if !errors.Is(err, ErrNoPreviewWorkers) || s.preferredRegion == "" {
		return WorkerNode{}, err
	}
	if repoID != uuid.Nil && len(placements) > 0 {
		if worker, ok, err := s.selectCachePlacementWorker(ctx, orgID, repoID, placements, false, req); err != nil {
			return WorkerNode{}, err
		} else if ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "cross_region")
			return worker, nil
		}
		if workerCachePlacementsApproximate(placements) {
			if worker, ok, err := s.selectRecentRepoCacheWorker(ctx, orgID, repoID, false, req); err != nil {
				metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "fallback_error")
			} else if ok {
				metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "cross_region")
				return worker, nil
			}
		}
		if worker, ok, err := s.selectRendezvousWorker(ctx, orgID, repoID, rendezvousPlacementKey(placements), rendezvousTopN, false, req); err != nil {
			return WorkerNode{}, err
		} else if ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "cross_region")
			return worker, nil
		}
	}
	if repoID != uuid.Nil && len(placements) == 0 {
		if worker, ok, err := s.selectRecentRepoCacheWorker(ctx, orgID, repoID, false, req); err != nil {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "fallback_error")
		} else if ok {
			metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "cross_region")
			return worker, nil
		}
	}
	metrics.RecordSessionDependencyCacheSchedulerDecision(ctx, orgID.String(), "cross_region")
	return s.selectLeastLoadedNode(ctx, nil, false, req)
}

// SelectLeastLoadedNode picks the preview-capable active worker with the fewest active previews.
func (s *WorkerSelector) SelectLeastLoadedNode(ctx context.Context) (WorkerNode, error) {
	return s.SelectLeastLoadedNodeExcept(ctx, nil)
}

func (s *WorkerSelector) SelectLeastLoadedNodeInPreferredRegion(ctx context.Context) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, nil, true, WorkerSelectionRequirements{})
}

// SelectLeastLoadedNodeExcept picks the least-loaded preview-capable active
// worker while skipping any excluded worker IDs.
func (s *WorkerSelector) SelectLeastLoadedNodeExcept(ctx context.Context, excluded map[string]struct{}) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, excluded, false, WorkerSelectionRequirements{})
}

// SelectLeastLoadedNodeWithRequirements picks the least-loaded worker that
// satisfies the requested runtime capabilities.
func (s *WorkerSelector) SelectLeastLoadedNodeWithRequirements(ctx context.Context, req WorkerSelectionRequirements) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, nil, false, req)
}

// SelectLeastLoadedNodeExceptWithRequirements picks the least-loaded worker
// that satisfies the requested runtime capabilities while skipping excluded IDs.
func (s *WorkerSelector) SelectLeastLoadedNodeExceptWithRequirements(ctx context.Context, excluded map[string]struct{}, req WorkerSelectionRequirements) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, excluded, false, req)
}

// HasStaticEgressCapableWorker reports whether all active workers that can
// claim session jobs are verified for static egress. Session jobs are claimed
// from the generic jobs queue, so mixed-capability worker fleets cannot safely
// expose the org setting as available.
func (s *WorkerSelector) HasStaticEgressCapableWorker(ctx context.Context, publicIP string) (bool, error) {
	diagnostics, err := s.StaticEgressWorkerDiagnostics(ctx, publicIP)
	if err != nil {
		return false, err
	}
	return diagnostics.Available, nil
}

func (s *WorkerSelector) StaticEgressWorkerDiagnostics(ctx context.Context, publicIP string) (StaticEgressWorkerDiagnostics, error) {
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return StaticEgressWorkerDiagnostics{}, err
	}
	diagnostics := StaticEgressWorkerDiagnostics{Available: true}
	hasSessionWorker := false
	for _, node := range nodes {
		if !nodeCanClaimSessionJobs(node) {
			continue
		}
		hasSessionWorker = true
		metadata, err := parseWorkerNodeMetadata(node)
		if err != nil {
			diagnostics.Available = false
			diagnostics.Mismatches = append(diagnostics.Mismatches, StaticEgressWorkerMismatch{
				NodeID: node.ID,
				Host:   node.Host,
				Mode:   string(node.Mode),
				Reason: "invalid worker metadata",
			})
			continue
		}
		if !workerMetadataMatchesStaticEgress(metadata, publicIP) {
			diagnostics.Available = false
			diagnostics.Mismatches = append(diagnostics.Mismatches, StaticEgressWorkerMismatch{
				NodeID:               node.ID,
				Host:                 node.Host,
				Mode:                 string(node.Mode),
				StaticEgressCapable:  metadata.StaticEgressCapable,
				StaticEgressPublicIP: metadata.StaticEgressPublicIP,
				Reason:               staticEgressMismatchReason(metadata, publicIP),
			})
		}
	}
	if !hasSessionWorker {
		diagnostics.Available = false
		diagnostics.Mismatches = append(diagnostics.Mismatches, StaticEgressWorkerMismatch{
			Reason: "no active session workers",
		})
	}
	return diagnostics, nil
}

func (s *WorkerSelector) selectLeastLoadedNode(ctx context.Context, excluded map[string]struct{}, preferredOnly bool, req WorkerSelectionRequirements) (WorkerNode, error) {
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return WorkerNode{}, err
	}

	// First pass: collect eligible workers.
	var eligible []WorkerNode
	for _, node := range nodes {
		if _, skip := excluded[node.ID]; skip {
			continue
		}
		worker, err := parseWorkerNodeWithRequirements(node, req)
		if err != nil {
			continue
		}
		if !nodeCanClaimSessionJobs(node) {
			continue
		}
		if preferredOnly && !s.inPreferredRegion(worker) {
			continue
		}
		eligible = append(eligible, worker)
	}
	if len(eligible) == 0 {
		return WorkerNode{}, ErrNoPreviewWorkers
	}

	// Fetch all counts in one query instead of N sequential round-trips.
	ids := make([]string, len(eligible))
	for i, w := range eligible {
		ids[i] = w.ID
	}
	counts, err := s.previews.CountActivePreviewsByWorkers(ctx, ids)
	if err != nil {
		return WorkerNode{}, fmt.Errorf("count active previews for workers: %w", err)
	}

	// Second pass: pick the least-loaded worker (ties broken by lexicographic ID).
	best := WorkerNode{}
	bestCount := 0
	found := false
	for i, worker := range eligible {
		count := counts[worker.ID]
		if count >= s.maxPreviewsPerWorker {
			continue
		}
		if !found || i == 0 || count < bestCount || (count == bestCount && worker.ID < best.ID) {
			best = worker
			bestCount = count
			found = true
		}
	}
	if !found {
		return WorkerNode{}, ErrNoPreviewWorkers
	}
	return best, nil
}

func (s *WorkerSelector) selectCachePlacementWorker(ctx context.Context, orgID, repoID uuid.UUID, placements []WorkerCachePlacement, preferredOnly bool, req WorkerSelectionRequirements) (WorkerNode, bool, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, s.cacheLookupTimeout)
	defer cancel()
	var locations []models.PreviewDependencyCacheLocation
	for _, placement := range normalizeWorkerCachePlacements(placements) {
		found, err := s.previews.ListDependencyCacheWorkersByPlacement(lookupCtx, orgID, repoID, placement.Kind, placement.PlacementKey, 64)
		if err != nil {
			return WorkerNode{}, false, err
		}
		locations = append(locations, found...)
	}
	if len(locations) == 0 {
		return WorkerNode{}, false, nil
	}
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return WorkerNode{}, false, err
	}
	routable := make(map[string]WorkerNode, len(nodes))
	for _, node := range nodes {
		worker, err := parseWorkerNodeWithRequirements(node, req)
		if err != nil || !nodeCanClaimSessionJobs(node) {
			continue
		}
		if preferredOnly && !s.inPreferredRegion(worker) {
			continue
		}
		routable[worker.ID] = worker
	}
	ids := make([]string, 0, len(locations))
	seen := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		if _, ok := routable[location.WorkerNodeID]; !ok {
			continue
		}
		if _, ok := seen[location.WorkerNodeID]; ok {
			continue
		}
		seen[location.WorkerNodeID] = struct{}{}
		ids = append(ids, location.WorkerNodeID)
	}
	if len(ids) == 0 {
		return WorkerNode{}, false, nil
	}
	counts, err := s.previews.CountActivePreviewsByWorkers(ctx, ids)
	if err != nil {
		return WorkerNode{}, false, err
	}
	best := WorkerNode{}
	bestCount := 0
	bestOrder := 0
	found := false
	for order, location := range locations {
		worker, ok := routable[location.WorkerNodeID]
		if !ok {
			continue
		}
		count := counts[worker.ID]
		if count >= s.maxPreviewsPerWorker {
			continue
		}
		if !found || count < bestCount || (count == bestCount && order < bestOrder) {
			best = worker
			bestCount = count
			bestOrder = order
			found = true
		}
	}
	if !found {
		return WorkerNode{}, false, nil
	}
	return best, true, nil
}

func (s *WorkerSelector) selectRecentRepoCacheWorker(ctx context.Context, orgID, repoID uuid.UUID, preferredOnly bool, req WorkerSelectionRequirements) (WorkerNode, bool, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, s.cacheLookupTimeout)
	defer cancel()
	locations, err := s.previews.ListRecentDependencyCacheWorkersForRepo(lookupCtx, orgID, repoID, 64)
	if err != nil {
		return WorkerNode{}, false, err
	}
	return s.selectCacheLocationWorker(ctx, locations, preferredOnly, req)
}

func (s *WorkerSelector) selectCacheLocationWorker(ctx context.Context, locations []models.PreviewDependencyCacheLocation, preferredOnly bool, req WorkerSelectionRequirements) (WorkerNode, bool, error) {
	if len(locations) == 0 {
		return WorkerNode{}, false, nil
	}
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return WorkerNode{}, false, err
	}
	routable := make(map[string]WorkerNode, len(nodes))
	for _, node := range nodes {
		worker, err := parseWorkerNodeWithRequirements(node, req)
		if err != nil || !nodeCanClaimSessionJobs(node) {
			continue
		}
		if preferredOnly && !s.inPreferredRegion(worker) {
			continue
		}
		routable[worker.ID] = worker
	}
	ids := make([]string, 0, len(locations))
	seen := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		if _, ok := routable[location.WorkerNodeID]; !ok {
			continue
		}
		if _, ok := seen[location.WorkerNodeID]; ok {
			continue
		}
		seen[location.WorkerNodeID] = struct{}{}
		ids = append(ids, location.WorkerNodeID)
	}
	if len(ids) == 0 {
		return WorkerNode{}, false, nil
	}
	counts, err := s.previews.CountActivePreviewsByWorkers(ctx, ids)
	if err != nil {
		return WorkerNode{}, false, err
	}
	best := WorkerNode{}
	bestCount := 0
	bestOrder := 0
	found := false
	for order, location := range locations {
		worker, ok := routable[location.WorkerNodeID]
		if !ok {
			continue
		}
		count := counts[worker.ID]
		if count >= s.maxPreviewsPerWorker {
			continue
		}
		if !found || count < bestCount || (count == bestCount && order < bestOrder) {
			best = worker
			bestCount = count
			bestOrder = order
			found = true
		}
	}
	if !found {
		return WorkerNode{}, false, nil
	}
	return best, true, nil
}

func normalizeWorkerCachePlacements(placements []WorkerCachePlacement) []WorkerCachePlacement {
	normalized := make([]WorkerCachePlacement, 0, len(placements))
	seen := make(map[string]struct{}, len(placements))
	for _, placement := range placements {
		key := strings.TrimSpace(placement.PlacementKey)
		if key == "" {
			continue
		}
		kind := placement.Kind
		if kind == "" {
			kind = models.PreviewCacheKindInstallArtifact
		}
		dedupe := string(kind) + "\x00" + key
		if _, ok := seen[dedupe]; ok {
			continue
		}
		seen[dedupe] = struct{}{}
		normalized = append(normalized, WorkerCachePlacement{Kind: kind, PlacementKey: key, Approximate: placement.Approximate})
	}
	return normalized
}

func workerCachePlacementsApproximate(placements []WorkerCachePlacement) bool {
	for _, placement := range placements {
		if placement.Approximate {
			return true
		}
	}
	return false
}

func rendezvousPlacementKey(placements []WorkerCachePlacement) string {
	placements = normalizeWorkerCachePlacements(placements)
	if len(placements) == 0 {
		return ""
	}
	parts := make([]string, 0, len(placements))
	for _, placement := range placements {
		parts = append(parts, string(placement.Kind)+":"+placement.PlacementKey)
	}
	return strings.Join(parts, "|")
}

func (s *WorkerSelector) selectRendezvousWorker(ctx context.Context, orgID, repoID uuid.UUID, placementKey string, topN int, preferredOnly bool, req WorkerSelectionRequirements) (WorkerNode, bool, error) {
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return WorkerNode{}, false, err
	}
	var eligible []WorkerNode
	for _, node := range nodes {
		worker, err := parseWorkerNodeWithRequirements(node, req)
		if err != nil || !nodeCanClaimSessionJobs(node) {
			continue
		}
		if preferredOnly && !s.inPreferredRegion(worker) {
			continue
		}
		eligible = append(eligible, worker)
	}
	if len(eligible) == 0 {
		return WorkerNode{}, false, ErrNoPreviewWorkers
	}
	sort.Slice(eligible, func(i, j int) bool {
		left := rendezvousScore(orgID, repoID, placementKey, eligible[i].ID)
		right := rendezvousScore(orgID, repoID, placementKey, eligible[j].ID)
		if left == right {
			return eligible[i].ID < eligible[j].ID
		}
		return left > right
	})
	if topN <= 0 || topN > len(eligible) {
		topN = len(eligible)
	}
	candidates := eligible[:topN]
	ids := make([]string, len(candidates))
	for i, worker := range candidates {
		ids[i] = worker.ID
	}
	counts, err := s.previews.CountActivePreviewsByWorkers(ctx, ids)
	if err != nil {
		return WorkerNode{}, false, err
	}
	for _, worker := range candidates {
		if counts[worker.ID] < s.maxPreviewsPerWorker {
			return worker, true, nil
		}
	}
	return WorkerNode{}, false, nil
}

func (s *WorkerSelector) inPreferredRegion(worker WorkerNode) bool {
	return s.preferredRegion == "" || worker.Region == s.preferredRegion
}

func rendezvousScore(orgID, repoID uuid.UUID, placementKey, workerID string) uint64 {
	sum := sha256.Sum256([]byte(orgID.String() + "\x00" + repoID.String() + "\x00" + placementKey + "\x00" + workerID))
	return binary.BigEndian.Uint64(sum[:8])
}
