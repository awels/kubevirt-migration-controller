/*
Copyright 2025 The KubeVirt Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package migmigration

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	migrations "kubevirt.io/kubevirt-migration-controller/api/migrationcontroller/v1alpha1"
	componenthelpers "kubevirt.io/kubevirt-migration-controller/pkg/component-helpers"
)

// MigMigrationReconciler reconciles a MigMigration object
type MigMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	record.EventRecorder
}

// +kubebuilder:rbac:groups=migrations.kubevirt.io,resources=migmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migrations.kubevirt.io,resources=migmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=migrations.kubevirt.io,resources=migmigrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=list;watch;update
// +kubebuilder:rbac:groups=core,resources=pods,verbs=list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MigMigration object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *MigMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the MigMigration instance
	migration := &migrations.MigMigration{}

	if err := r.Get(context.TODO(), req.NamespacedName, migration); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	origMigration := migration.DeepCopy()

	// Completed.
	if migration.Status.Phase == string(Completed) {
		return reconcile.Result{}, nil
	}

	plan, err := componenthelpers.GetPlan(ctx, r.Client, migration.Spec.MigPlanRef)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Owner Reference
	r.setOwnerReference(plan, migration)

	// Validate
	if err := r.validate(plan, migration); err != nil {
		return reconcile.Result{}, err
	}

	requeueAfter := NoReQ

	// Migrate
	if !migration.Status.HasBlockerCondition() {
		var err error
		requeueAfter, err = r.migrate(ctx, plan, migration)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// Apply changes.
	if !reflect.DeepEqual(migration, origMigration) {
		if err := r.Update(context.TODO(), migration); err != nil {
			return reconcile.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// Set the owner reference is set to the plan.
func (r *MigMigrationReconciler) setOwnerReference(plan *migrations.MigPlan, migration *migrations.MigMigration) {
	if plan == nil {
		return
	}
	for i := range migration.OwnerReferences {
		ref := &migration.OwnerReferences[i]
		if ref.Kind == plan.Kind {
			ref.APIVersion = plan.APIVersion
			ref.Name = plan.Name
			ref.UID = plan.UID
			return
		}
	}
	migration.OwnerReferences = append(
		migration.OwnerReferences,
		metav1.OwnerReference{
			APIVersion: plan.APIVersion,
			Kind:       plan.Kind,
			Name:       plan.Name,
			UID:        plan.UID,
		})
}

// SetupWithManager sets up the controller with the Manager.
func (r *MigMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a new controller
	c, err := controller.New("migmigration-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to MigMigration
	if err := c.Watch(source.Kind(mgr.GetCache(), &migrations.MigMigration{},
		&handler.TypedEnqueueRequestForObject[*migrations.MigMigration]{},
		predicate.TypedFuncs[*migrations.MigMigration]{
			CreateFunc: func(e event.TypedCreateEvent[*migrations.MigMigration]) bool { return true },
			DeleteFunc: func(e event.TypedDeleteEvent[*migrations.MigMigration]) bool { return true },
			UpdateFunc: func(e event.TypedUpdateEvent[*migrations.MigMigration]) bool {
				return !reflect.DeepEqual(e.ObjectOld.Spec, e.ObjectNew.Spec) ||
					!reflect.DeepEqual(e.ObjectOld.DeletionTimestamp, e.ObjectNew.DeletionTimestamp)
			},
		},
	)); err != nil {
		return err
	}

	return nil
}
