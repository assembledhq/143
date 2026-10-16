package codereview

import (
	"sync"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// SandboxAdmissionContractVersion reports configured wiring, not a build flag.
func (s *Service) SandboxAdmissionContractVersion() models.CodeReviewAdmissionContractVersion {
	if s == nil || !s.sandboxAdmission || !s.SchedulingEnabled() || s.scheduling.store == nil {
		return ""
	}
	return models.CodeReviewAdmissionContractV1
}

type AdmissionContractProvider interface {
	SandboxAdmissionContractVersion() models.CodeReviewAdmissionContractVersion
}

// AdmissionProcessEvidence joins the independently constructed API and worker
// lifecycle graphs. The instance changes on restart and carries no host identity.
// All server modes retain an HTTP API graph; worker/all additionally require
// their worker graph before advertising the process-wide protocol.
type AdmissionProcessEvidence struct {
	mu             sync.RWMutex
	mode           string
	instanceID     string
	buildSHA       string
	apiContract    models.CodeReviewAdmissionContractVersion
	workerContract models.CodeReviewAdmissionContractVersion
}

func NewAdmissionProcessEvidence(mode, buildSHA string) *AdmissionProcessEvidence {
	return &AdmissionProcessEvidence{mode: mode, instanceID: uuid.NewString(), buildSHA: buildSHA}
}

func (p *AdmissionProcessEvidence) ConfigureAPI(service AdmissionContractProvider) {
	var version models.CodeReviewAdmissionContractVersion
	if service != nil {
		version = service.SandboxAdmissionContractVersion()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.apiContract = version
}

func (p *AdmissionProcessEvidence) ConfigureWorker(service any) {
	var version models.CodeReviewAdmissionContractVersion
	if provider, ok := service.(AdmissionContractProvider); ok {
		version = provider.SandboxAdmissionContractVersion()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workerContract = version
}

func (p *AdmissionProcessEvidence) Evidence() models.CodeReviewAdmissionProcessEvidence {
	p.mu.RLock()
	defer p.mu.RUnlock()
	evidence := models.CodeReviewAdmissionProcessEvidence{InstanceID: p.instanceID, BuildSHA: p.buildSHA}
	ready := p.apiContract == models.CodeReviewAdmissionContractV1
	switch p.mode {
	case "api":
	case "worker", "all":
		ready = ready && p.workerContract == models.CodeReviewAdmissionContractV1
	default:
		ready = false
	}
	if ready {
		evidence.ContractVersion = models.CodeReviewAdmissionContractV1
	}
	return evidence
}
