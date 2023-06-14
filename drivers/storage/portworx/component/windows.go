package component

import (
	"context"
	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	WindowsComponentName     = "Windows Node"
	WindowsDaemonSetName     = "csi-pwx-node-win"
	WindowsDaemonSetFileName = "win.yaml"
)

type windows struct {
	client        client.Client
	isCreated     bool
	isWindowsNode bool
}

func (w *windows) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	w.client = k8sClient
}

func (w *windows) Name() string {
	return WindowsComponentName
}

func (w *windows) Priority() int32 {
	return DefaultComponentPriority
}

func (w *windows) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (w *windows) IsEnabled(cluster *corev1.StorageCluster) bool {
	w.isWindowsNode = true
	return w.isWindowsNode
}

func (w *windows) Reconcile(cluster *corev1.StorageCluster) error {
	ownRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	if err := w.createDaemonSet(WindowsDaemonSetFileName, ownRef, cluster); err != nil {
		logrus.Errorf("error during creating %s daemonset %s ", WindowsDaemonSetName, err)
		return err
	}

	return nil
}

func (w *windows) Delete(cluster *corev1.StorageCluster) error {

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8s.DeleteDaemonSet(w.client, WindowsDaemonSetName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	w.MarkDeleted()
	return nil
}

func (w *windows) MarkDeleted() {
	w.isCreated = false
}

// RegisterPortworxPluginComponent registers the PortworxPlugin component
func RegisterWindowsComponent() {
	Register(WindowsComponentName, &windows{})
}

func init() {
	RegisterWindowsComponent()
}

func (w *windows) createDaemonSet(filename string, ownerRef *metav1.OwnerReference, cluster *corev1.StorageCluster) error {
	daemonSet, err := k8s.GetDaemonSetFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}

	existingDaemonSet := &appsv1.DaemonSet{}
	getErr := w.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      WindowsDaemonSetName,
			Namespace: cluster.Namespace,
		},
		existingDaemonSet,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &daemonSet.Spec.Template.Spec)

	equal, _ := util.DeepEqualPodTemplate(&daemonSet.Spec.Template, &existingDaemonSet.Spec.Template)
	if !equal && w.isWindowsNode {
		if err := k8s.CreateOrUpdateDaemonSet(w.client, daemonSet, ownerRef); err != nil {
			return err
		}
	}
	w.isCreated = true
	return nil
}
