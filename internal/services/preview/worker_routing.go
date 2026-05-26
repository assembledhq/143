package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

var (
	// ErrNoPreviewWorkers reports that no active preview-capable worker is available.
	ErrNoPreviewWorkers = errors.New("no active preview-capable worker available")
	// ErrLegacySessionWorkerOwnership reports that a live session predates
	// worker ownership tracking, so preview reuse cannot be routed safely.
	ErrLegacySessionWorkerOwnership = errors.New("live session is missing worker ownership metadata")
)

// WorkerNodeMetadata is the routable subset of nodes.metadata used by preview.
type WorkerNodeMetadata struct {
	BuildSHA               string `json:"build_sha,omitempty"`
	PreviewCapable         bool   `json:"preview_capable,omitempty"`
	PreviewInternalBaseURL string `json:"preview_internal_base_url,omitempty"`
	StaticEgressCapable    bool   `json:"static_egress_capable,omitempty"`
}

// WorkerNode is a preview-routable worker node.
type WorkerNode struct {
	ID      string
	Mode    string
	BaseURL string
}

// WorkerSelectionRequirements constrains cold-start worker selection.
type WorkerSelectionRequirements struct {
	StaticEgressRequired bool
}

// WorkerSelector resolves preview-owning workers and selects workers for cold starts.
type WorkerSelector struct {
	nodes    *db.NodeStore
	previews *db.PreviewStore
}

// NewWorkerSelector creates a new worker selector.
func NewWorkerSelector(nodes *db.NodeStore, previews *db.PreviewStore) *WorkerSelector {
	return &WorkerSelector{nodes: nodes, previews: previews}
}

func parseWorkerNode(node models.Node) (WorkerNode, error) {
	var metadata WorkerNodeMetadata
	if len(node.Metadata) > 0 {
		if err := json.Unmarshal(node.Metadata, &metadata); err != nil {
			return WorkerNode{}, fmt.Errorf("parse node metadata: %w", err)
		}
	}
	if !metadata.PreviewCapable {
		return WorkerNode{}, fmt.Errorf("node %s is not preview-capable", node.ID)
	}
	baseURL := strings.TrimRight(metadata.PreviewInternalBaseURL, "/")
	if baseURL == "" {
		return WorkerNode{}, fmt.Errorf("node %s has no preview internal base url", node.ID)
	}
	return WorkerNode{
		ID:      node.ID,
		Mode:    string(node.Mode),
		BaseURL: baseURL,
	}, nil
}

func parseWorkerNodeWithRequirements(node models.Node, req WorkerSelectionRequirements) (WorkerNode, error) {
	var metadata WorkerNodeMetadata
	if len(node.Metadata) > 0 {
		if err := json.Unmarshal(node.Metadata, &metadata); err != nil {
			return WorkerNode{}, fmt.Errorf("parse node metadata: %w", err)
		}
	}
	if req.StaticEgressRequired && !metadata.StaticEgressCapable {
		return WorkerNode{}, fmt.Errorf("node %s is not static-egress capable", node.ID)
	}
	return parseWorkerNode(node)
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
	}, nil
}

func isResolvableNodeStatus(status models.NodeStatus) bool {
	return status == models.NodeStatusActive || status == models.NodeStatusDraining
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
		return WorkerNode{}, fmt.Errorf("node %s is not routable", nodeID)
	}
	return parseRoutableWorkerNode(*node)
}

// SelectStartNode picks the worker that should handle Start Preview for the session.
func (s *WorkerSelector) SelectStartNode(ctx context.Context, orgID uuid.UUID, session *models.Session) (WorkerNode, error) {
	return s.SelectStartNodeWithRequirements(ctx, orgID, session, WorkerSelectionRequirements{})
}

// SelectStartNodeWithRequirements picks the worker that should handle Start
// Preview for the session while honoring optional runtime capabilities.
func (s *WorkerSelector) SelectStartNodeWithRequirements(ctx context.Context, orgID uuid.UUID, session *models.Session, req WorkerSelectionRequirements) (WorkerNode, error) {
	if session == nil {
		return WorkerNode{}, fmt.Errorf("session is required")
	}

	instance, err := s.previews.GetActivePreviewForSession(ctx, orgID, session.ID)
	if err == nil && instance != nil {
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
		return s.ResolveNode(ctx, *session.WorkerNodeID)
	}

	return s.SelectLeastLoadedNodeWithRequirements(ctx, req)
}

// SelectLeastLoadedNode picks the preview-capable active worker with the fewest active previews.
func (s *WorkerSelector) SelectLeastLoadedNode(ctx context.Context) (WorkerNode, error) {
	return s.SelectLeastLoadedNodeExcept(ctx, nil)
}

// SelectLeastLoadedNodeExcept picks the least-loaded preview-capable active
// worker while skipping any excluded worker IDs.
func (s *WorkerSelector) SelectLeastLoadedNodeExcept(ctx context.Context, excluded map[string]struct{}) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, excluded, WorkerSelectionRequirements{})
}

// SelectLeastLoadedNodeWithRequirements picks the least-loaded worker that
// satisfies the requested runtime capabilities.
func (s *WorkerSelector) SelectLeastLoadedNodeWithRequirements(ctx context.Context, req WorkerSelectionRequirements) (WorkerNode, error) {
	return s.selectLeastLoadedNode(ctx, nil, req)
}

// HasStaticEgressCapableWorker reports whether at least one active worker can
// cold-start static-egress sandboxes.
func (s *WorkerSelector) HasStaticEgressCapableWorker(ctx context.Context) (bool, error) {
	nodes, err := s.nodes.ListActive(ctx)
	if err != nil {
		return false, err
	}
	for _, node := range nodes {
		worker, err := parseWorkerNodeWithRequirements(node, WorkerSelectionRequirements{StaticEgressRequired: true})
		if err != nil {
			continue
		}
		if worker.Mode == "worker" || worker.Mode == "all" {
			return true, nil
		}
	}
	return false, nil
}

func (s *WorkerSelector) selectLeastLoadedNode(ctx context.Context, excluded map[string]struct{}, req WorkerSelectionRequirements) (WorkerNode, error) {
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
		if worker.Mode != "worker" && worker.Mode != "all" {
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
	for i, worker := range eligible {
		count := counts[worker.ID]
		if i == 0 || count < bestCount || (count == bestCount && worker.ID < best.ID) {
			best = worker
			bestCount = count
		}
	}
	return best, nil
}
