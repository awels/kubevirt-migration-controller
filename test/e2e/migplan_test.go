package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	migrations "kubevirt.io/kubevirt-migration-controller/api/migrationcontroller/v1alpha1"
	"kubevirt.io/kubevirt-migration-controller/internal/controller/migplan"
)

var _ = Describe("MigPlan", func() {
	var (
		namespace string
	)

	BeforeEach(func() {
		By("Creating a new test namespace")
		namespace = "e2e-test-migplan-" + rand.String(6)
		_, err := kcs.CoreV1().Namespaces().Create(context.TODO(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		By("Deleting the test migplan")
		Eventually(func() bool {
			err := mcs.MigrationcontrollerV1alpha1().MigPlans(namespace).Delete(context.TODO(),
				"test-plan", metav1.DeleteOptions{})
			if k8serrors.IsNotFound(err) {
				return true
			}
			Expect(err).ToNot(HaveOccurred())
			return k8serrors.IsNotFound(err)
		}, 30*time.Second, time.Second).Should(BeTrue())

		By("Deleting the test namespace")
		Eventually(func() bool {
			err := kcs.CoreV1().Namespaces().Delete(context.TODO(), namespace, metav1.DeleteOptions{})
			if k8serrors.IsNotFound(err) {
				return true
			}
			Expect(err).ToNot(HaveOccurred())
			return k8serrors.IsNotFound(err)
		}, 30*time.Second, time.Second).Should(BeTrue())
	})

	It("plan should be marked as not ready when VM is missing", func() {
		plan := &migrations.MigPlan{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-plan",
				Namespace: namespace,
			},
			Spec: migrations.MigPlanSpec{
				VirtualMachines: []migrations.MigPlanVirtualMachine{
					{
						Name: "test-vm",
						MigrationPVCs: []migrations.MigPlanMigrationPVC{
							{
								VolumeName: "test-volume",
								DestinationPVC: migrations.MigPlanDestinationPVC{
									Name:         "test-pvc",
									StorageClass: "test-storage-class",
									AccessModes:  []migrations.MigPlanAccessMode{"ReadWriteOnce"},
									VolumeMode:   "Filesystem",
								},
							},
						},
					},
				},
			},
		}
		_, err := mcs.MigrationcontrollerV1alpha1().MigPlans(namespace).Create(context.TODO(),
			plan, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() bool {
			plan, err := mcs.MigrationcontrollerV1alpha1().MigPlans(namespace).Get(context.TODO(),
				"test-plan", metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			return plan.Status.HasCriticalCondition(migplan.StorageMigrationNotPossible)
		}, 30*time.Second, time.Second).Should(BeTrue())
	})
})
