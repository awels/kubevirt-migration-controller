package migmigration

import (
	"fmt"
	"path"

	migrations "kubevirt.io/kubevirt-migration-controller/api/migrationcontroller/v1alpha1"
)

// Statuses
const (
	True  = migrations.True
	False = migrations.False
)

// Categories
const (
	Critical = migrations.Critical
	Advisory = migrations.Advisory
)

// Reasons
const (
	NotSet         = "NotSet"
	NotFound       = "NotFound"
	Cancel         = "Cancel"
	ErrorsDetected = "ErrorsDetected"
	NotSupported   = "NotSupported"
	PvNameConflict = "PvNameConflict"
	NotDistinct    = "NotDistinct"
)

// Types
const (
	UnhealthyNamespaces                = "UnhealthyNamespaces"
	InvalidPlanRef                     = "InvalidPlanRef"
	PlanNotReady                       = "PlanNotReady"
	PlanClosed                         = "PlanClosed"
	HasFinalMigration                  = "HasFinalMigration"
	Postponed                          = "Postponed"
	SucceededWithWarnings              = "SucceededWithWarnings"
	ResticErrors                       = "ResticErrors"
	ResticVerifyErrors                 = "ResticVerifyErrors"
	VeleroInitialBackupPartiallyFailed = "VeleroInitialBackupPartiallyFailed"
	VeleroStageBackupPartiallyFailed   = "VeleroStageBackupPartiallyFailed"
	VeleroStageRestorePartiallyFailed  = "VeleroStageRestorePartiallyFailed"
	VeleroFinalRestorePartiallyFailed  = "VeleroFinalRestorePartiallyFailed"
	DirectImageMigrationFailed         = "DirectImageMigrationFailed"
	StageNoOp                          = "StageNoOp"
	RegistriesHealthy                  = "RegistriesHealthy"
	RegistriesUnhealthy                = "RegistriesUnhealthy"
	StaleSrcVeleroCRsDeleted           = "StaleSrcVeleroCRsDeleted"
	StaleDestVeleroCRsDeleted          = "StaleDestVeleroCRsDeleted"
	StaleResticCRsDeleted              = "StaleResticCRsDeleted"
	DirectVolumeMigrationBlocked       = "DirectVolumeMigrationBlocked"
	InvalidSpec                        = "InvalidSpec"
	ConflictingPVCMappings             = "ConflictingPVCMappings"
)

// Validate the migration resource.
func (r *MigMigrationReconciler) validate(plan *migrations.MigPlan, migration *migrations.MigMigration) error {
	// verify the migration is owned by a plan
	ownerReference := migration.FindOwnerReference()
	if ownerReference == nil {
		return fmt.Errorf("migration is not owned by a plan")
	} else if ownerReference.UID != migration.Spec.MigPlanRef.UID {
		return fmt.Errorf("migration is not owned by the plan %s, uid mismatch", migration.Spec.MigPlanRef.Name)
	}
	return r.validatePlan(plan, migration)
}

// Validate the referenced plan.
func (r *MigMigrationReconciler) validatePlan(plan *migrations.MigPlan, migration *migrations.MigMigration) error {
	if plan == nil {
		migration.Status.SetCondition(migrations.Condition{
			Type:     InvalidPlanRef,
			Status:   True,
			Reason:   NotFound,
			Category: Critical,
			Message: fmt.Sprintf("The referenced `migPlanRef` does not exist, subject: %s.",
				path.Join(migration.Spec.MigPlanRef.Namespace, migration.Spec.MigPlanRef.Name)),
		})
		return nil
	}
	// Check if the plan has any critical conditions
	if plan.Status.HasCriticalCondition() {
		migration.Status.SetCondition(migrations.Condition{
			Type:     PlanNotReady,
			Status:   True,
			Category: Critical,
			Message: fmt.Sprintf("The referenced `migPlanRef` does not have a `Ready` condition, subject: %s.",
				path.Join(migration.Spec.MigPlanRef.Namespace, migration.Spec.MigPlanRef.Name)),
		})
		return nil
	}
	return nil
}
