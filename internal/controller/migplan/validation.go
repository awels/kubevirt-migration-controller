package migplan

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	virtv1 "kubevirt.io/api/core/v1"
	migrations "kubevirt.io/kubevirt-migration-controller/api/migrationcontroller/v1alpha1"
	componenthelpers "kubevirt.io/kubevirt-migration-controller/pkg/component-helpers"
)

// Types
const (
	KubeVirtNotInstalledSourceCluster      = "KubeVirtNotInstalledSourceCluster"
	KubeVirtVersionNotSupported            = "KubeVirtVersionNotSupported"
	KubeVirtStorageLiveMigrationNotEnabled = "KubeVirtStorageLiveMigrationNotEnabled"
	StorageMigrationNotPossible            = "StorageMigrationNotPossible"
	InvalidPVCs                            = "InvalidPVCs"
	StorageLiveMigratable                  = "StorageLiveMigratable"
)

// Categories
const (
	Advisory = migrations.Advisory
	Critical = migrations.Critical
	Error    = migrations.Error
	Warn     = migrations.Warn
)

// Reasons
const (
	NotSet                 = "NotSet"
	NotFound               = "NotFound"
	NotReady               = "NotReady"
	KeyNotFound            = "KeyNotFound"
	NotDistinct            = "NotDistinct"
	LimitExceeded          = "LimitExceeded"
	LengthExceeded         = "LengthExceeded"
	NotDNSCompliant        = "NotDNSCompliant"
	NotDone                = "NotDone"
	IsDone                 = "Done"
	Conflict               = "Conflict"
	NotHealthy             = "NotHealthy"
	NodeSelectorsDetected  = "NodeSelectorsDetected"
	DuplicateNs            = "DuplicateNamespaces"
	ConflictingNamespaces  = "ConflictingNamespaces"
	ConflictingPermissions = "ConflictingPermissions"
	NotSupported           = "NotSupported"
)

// Messages
const (
	KubeVirtNotInstalledSourceClusterMessage      = "KubeVirt is not installed on the source cluster"
	KubeVirtVersionNotSupportedMessage            = "KubeVirt version does not support storage live migration, Virtual Machines will be stopped instead"
	KubeVirtStorageLiveMigrationNotEnabledMessage = "KubeVirt storage live migration is not enabled, Virtual Machines will be stopped instead"
)

// Statuses
const (
	True  = migrations.True
	False = migrations.False
)

// Valid kubevirt feature gates
const (
	VolumesUpdateStrategy = "VolumesUpdateStrategy"
	VolumeMigrationConfig = "VolumeMigration"
	VMLiveUpdateFeatures  = "VMLiveUpdateFeatures"
	storageProfile        = "auto"
)

// Validate the plan resource.
func (r *MigPlanReconciler) validate(ctx context.Context, plan *migrations.MigPlan) error {
	if err := r.validateLiveMigrationPossible(ctx, plan); err != nil {
		return fmt.Errorf("err checking if live migration is possible: %w", err)
	}

	return nil
}

func (r *MigPlanReconciler) validateLiveMigrationPossible(ctx context.Context, plan *migrations.MigPlan) error {
	if err := r.validateKubeVirtInstalled(ctx, plan); err != nil {
		return err
	}
	if err := r.validateStorageMigrationPossible(ctx, plan); err != nil {
		return err
	}
	return nil
}

func (r *MigPlanReconciler) validateStorageMigrationPossible(ctx context.Context, plan *migrations.MigPlan) error {
	// Loop over the virtual machines in the plan and validate if the storage migration is possible.
	for _, vm := range plan.Spec.VirtualMachines {
		if reason, message, err := r.validateStorageMigrationPossibleForVM(ctx, &vm, plan.Namespace); err != nil {
			return err
		} else if message != "" {
			plan.Status.SetCondition(migrations.Condition{
				Type:     StorageMigrationNotPossible,
				Status:   True,
				Reason:   reason,
				Category: Critical,
				Message:  message,
			})
			return nil
		}
		// Check if the PVCs in the migplan match the PVCs in the virtual machine.
		if reason, message, err := r.validatePVCsMatch(ctx, &vm, plan.Namespace); err != nil {
			return err
		} else if message != "" {
			plan.Status.SetCondition(migrations.Condition{
				Type:     StorageMigrationNotPossible,
				Status:   True,
				Reason:   reason,
				Category: Critical,
				Message:  message,
			})
			return nil
		}
		// Add the virtual machine to the ready migrations if it is not completed.
		if !r.isVMCompleted(plan, &vm) && !r.isVMInProgress(plan, &vm) && !r.isVMFailed(plan, &vm) {
			plan.Status.ReadyMigrations = append(plan.Status.ReadyMigrations, vm)
		}
	}
	// Validate the PVCs are valid
	if reason, message, err := r.validatePVCs(ctx, plan); err != nil {
		return err
	} else if message != "" {
		plan.Status.SetCondition(migrations.Condition{
			Type:     InvalidPVCs,
			Status:   True,
			Reason:   reason,
			Category: Critical,
			Message:  message,
		})
		return nil
	}
	// Remove the storage migration not possible condition for the virtual machine.
	plan.Status.DeleteCondition(StorageMigrationNotPossible)
	return nil
}

func (r *MigPlanReconciler) isVMCompleted(plan *migrations.MigPlan, vm *migrations.MigPlanVirtualMachine) bool {
	for _, completedVM := range plan.Status.CompletedMigrations {
		if completedVM.Name == vm.Name {
			return true
		}
	}
	return false
}

func (r *MigPlanReconciler) isVMInProgress(plan *migrations.MigPlan, vm *migrations.MigPlanVirtualMachine) bool {
	for _, inProgressVM := range plan.Status.InProgressMigrations {
		if inProgressVM.Name == vm.Name {
			return true
		}
	}
	return false
}

func (r *MigPlanReconciler) isVMFailed(plan *migrations.MigPlan, vm *migrations.MigPlanVirtualMachine) bool {
	for _, failedVM := range plan.Status.FailedMigrations {
		if failedVM.Name == vm.Name {
			return true
		}
	}
	return false
}

func (r *MigPlanReconciler) validatePVCs(ctx context.Context, plan *migrations.MigPlan) (string, string, error) {
	for _, vm := range plan.Spec.VirtualMachines {
		for _, pvc := range vm.MigrationPVCs {
			if pvc.DestinationPVC.StorageClass != "" {
				// Validate the storage class exists in the cluster
				if reason, message, err := r.validateStorageClassExists(ctx, pvc.DestinationPVC.StorageClass); err != nil {
					return "", "", err
				} else if message != "" {
					return reason, message, nil
				}
			} else {
				// Validate a default storage class exists in the cluster.
				if reason, message, err := r.validateDefaultStorageClassExists(ctx); err != nil {
					return "", "", err
				} else if message != "" {
					return reason, message, nil
				}
			}
		}
	}
	return "", "", nil
}

func (r *MigPlanReconciler) validateStorageClassExists(ctx context.Context, storageClass string) (string, string, error) {
	sc := &storagev1.StorageClass{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: storageClass}, sc); err != nil {
		if k8serrors.IsNotFound(err) {
			return NotFound, fmt.Sprintf("storage class %s not found", storageClass), nil
		}
		return "", "", err
	}
	return "", "", nil
}

func (r *MigPlanReconciler) validateDefaultStorageClassExists(ctx context.Context) (string, string, error) {
	defaultStorageClass, err := componenthelpers.GetDefaultStorageClass(ctx, r.Client)
	if err != nil {
		return "", "", err
	}
	if defaultStorageClass != nil {
		return "", "", nil
	}
	virtStorageClass, err := componenthelpers.GetDefaultVirtStorageClass(ctx, r.Client)
	if err != nil {
		return "", "", err
	}
	if virtStorageClass != nil {
		return "", "", nil
	}
	return NotFound, "no default storage class found", nil
}

func (r *MigPlanReconciler) validatePVCsMatch(ctx context.Context, migplanVM *migrations.MigPlanVirtualMachine, namespace string) (string, string, error) {
	vm := virtv1.VirtualMachine{}
	log := logf.FromContext(ctx)
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: migplanVM.Name}, &vm); err != nil {
		if k8serrors.IsNotFound(err) {
			return NotFound, "virtual machine not found", nil
		}
		return "", "", err
	}

	vmVolumeMap := make(map[string]virtv1.Volume)
	for _, volume := range vm.Spec.Template.Spec.Volumes {
		vmVolumeMap[volume.Name] = volume
	}
	for _, volume := range migplanVM.MigrationPVCs {
		log.Info("Validating volume", "volume", volume.VolumeName)
		if _, ok := vmVolumeMap[volume.VolumeName]; !ok {
			return NotFound, fmt.Sprintf("volume %s not found in virtual machine", volume.VolumeName), nil
		}
		log.Info("Volume found in virtual machine", "volume", volume.VolumeName)
	}
	return "", "", nil
}

func (r *MigPlanReconciler) validateStorageMigrationPossibleForVM(ctx context.Context, planVM *migrations.MigPlanVirtualMachine, namespace string) (string, string, error) {
	// Check the conditions for the virtual machine.
	vm := virtv1.VirtualMachine{}
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: planVM.Name}, &vm); err != nil {
		if k8serrors.IsNotFound(err) {
			return NotFound, "virtual machine not found", nil
		}
		return "", "", err
	}
	if !vm.Status.Ready {
		return NotReady, "virtual machine is not ready", nil
	}
	for _, condition := range vm.Status.Conditions {
		if condition.Type == StorageLiveMigratable && condition.Status == False {
			return condition.Reason, condition.Message, nil
		}
	}
	return "", "", nil
}

func (r *MigPlanReconciler) validateKubeVirtInstalled(ctx context.Context, plan *migrations.MigPlan) error {
	log := logf.FromContext(ctx)
	kubevirtList := &virtv1.KubeVirtList{}
	if err := r.Client.List(ctx, kubevirtList); err != nil {
		if meta.IsNoMatchError(err) {
			return nil
		}
		return fmt.Errorf("error listing kubevirts: %w", err)
	}
	if len(kubevirtList.Items) == 0 || len(kubevirtList.Items) > 1 {
		plan.Status.SetCondition(migrations.Condition{
			Type:     KubeVirtNotInstalledSourceCluster,
			Status:   True,
			Reason:   NotFound,
			Category: Critical,
			Message:  KubeVirtNotInstalledSourceClusterMessage,
		})
		return nil
	}
	kubevirt := kubevirtList.Items[0]
	operatorVersion := kubevirt.Status.OperatorVersion
	major, minor, bugfix, err := parseKubeVirtOperatorSemver(operatorVersion)
	if err != nil {
		plan.Status.SetCondition(migrations.Condition{
			Type:     KubeVirtVersionNotSupported,
			Status:   True,
			Reason:   NotSupported,
			Category: Critical,
			Message:  KubeVirtVersionNotSupportedMessage,
		})
		return nil
	}
	log.V(3).Info("KubeVirt operator version", "major", major, "minor", minor, "bugfix", bugfix)
	// Check if kubevirt operator version is at least 1.3.0 if live migration is enabled.
	if major < 1 || (major == 1 && minor < 3) {
		plan.Status.SetCondition(migrations.Condition{
			Type:     KubeVirtVersionNotSupported,
			Status:   True,
			Reason:   NotSupported,
			Category: Critical,
			Message:  KubeVirtVersionNotSupportedMessage,
		})
		return nil
	}
	// Check if the appropriate feature gates are enabled
	if kubevirt.Spec.Configuration.VMRolloutStrategy == nil ||
		*kubevirt.Spec.Configuration.VMRolloutStrategy != virtv1.VMRolloutStrategyLiveUpdate ||
		kubevirt.Spec.Configuration.DeveloperConfiguration == nil ||
		isStorageLiveMigrationDisabled(&kubevirt, major, minor) {
		plan.Status.SetCondition(migrations.Condition{
			Type:     KubeVirtStorageLiveMigrationNotEnabled,
			Status:   True,
			Reason:   NotSupported,
			Category: Critical,
			Message:  KubeVirtStorageLiveMigrationNotEnabledMessage,
		})
		return nil
	}
	return nil
}

func parseKubeVirtOperatorSemver(operatorVersion string) (int, int, int, error) {
	// example versions: v1.1.1-106-g0be1a2073, or: v1.3.0-beta.0.202+f8efa57713ba76-dirty
	tokens := strings.Split(operatorVersion, ".")
	if len(tokens) < 3 {
		return -1, -1, -1, fmt.Errorf("version string was not in semver format, != 3 tokens")
	}

	if tokens[0][0] == 'v' {
		tokens[0] = tokens[0][1:]
	}
	major, err := strconv.Atoi(tokens[0])
	if err != nil {
		return -1, -1, -1, fmt.Errorf("major version could not be parsed as integer")
	}

	minor, err := strconv.Atoi(tokens[1])
	if err != nil {
		return -1, -1, -1, fmt.Errorf("minor version could not be parsed as integer")
	}

	bugfixTokens := strings.Split(tokens[2], "-")
	bugfix, err := strconv.Atoi(bugfixTokens[0])
	if err != nil {
		return -1, -1, -1, fmt.Errorf("bugfix version could not be parsed as integer")
	}

	return major, minor, bugfix, nil
}

func isStorageLiveMigrationDisabled(kubevirt *virtv1.KubeVirt, major, minor int) bool {
	if major == 1 && minor >= 5 || major > 1 {
		// Those are all GA from 1.5 and onwards
		// https://github.com/kubevirt/kubevirt/releases/tag/v1.5.0
		return false
	}

	return !slices.Contains(kubevirt.Spec.Configuration.DeveloperConfiguration.FeatureGates, VolumesUpdateStrategy) ||
		!slices.Contains(kubevirt.Spec.Configuration.DeveloperConfiguration.FeatureGates, VolumeMigrationConfig) ||
		!slices.Contains(kubevirt.Spec.Configuration.DeveloperConfiguration.FeatureGates, VMLiveUpdateFeatures)
}
