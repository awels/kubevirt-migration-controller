package install

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	migrationcontroller "kubevirt.io/kubevirt-migration-controller/api/migrationcontroller/v1alpha1"
)

func Install(scheme *runtime.Scheme) {
	utilruntime.Must(migrationcontroller.AddToScheme(scheme))
}
