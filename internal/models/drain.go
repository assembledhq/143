package models

import "fmt"

type DrainIntent string

const (
	DrainIntentNone                 DrainIntent = "none"
	DrainIntentPlannedRollout       DrainIntent = "planned_rollout"
	DrainIntentDeployBudgetExpired  DrainIntent = "deploy_budget_expired"
	DrainIntentRuntimeCeiling       DrainIntent = "runtime_ceiling"
	DrainIntentHumanInputCheckpoint DrainIntent = "human_input_checkpoint"
	DrainIntentHostMaintenance      DrainIntent = "host_maintenance"
	DrainIntentEmergencyForce       DrainIntent = "emergency_force"
)

func (i DrainIntent) Validate() error {
	switch i {
	case DrainIntentNone,
		DrainIntentPlannedRollout,
		DrainIntentDeployBudgetExpired,
		DrainIntentRuntimeCeiling,
		DrainIntentHumanInputCheckpoint,
		DrainIntentHostMaintenance,
		DrainIntentEmergencyForce:
		return nil
	default:
		return fmt.Errorf("invalid DrainIntent: %q", i)
	}
}
