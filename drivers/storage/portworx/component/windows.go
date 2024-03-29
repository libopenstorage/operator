package component

import (
	"context"
	commonerrors "errors"
	"strings"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/apis/core"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	WindowsComponentName           = "Windows Node"
	WindowsCsiDaemonsetName        = "px-csi-node-win"
	WindowsDriverDaemonsetName     = "px-csi-win-driver"
	WindowsStorageClass            = "px-csi-win-shared"
	WindowsCsiDaemonsetFileName    = "px-csi-node-win.yaml"
	WindowsDriverDaemonsetFileName = "px-csi-win-driver.yaml"
)

type windows struct {
	client             client.Client
	k8sVersion         *version.Version
	isCsiCreated       bool
	isInstallerCreated bool
	isWindowsNode      bool
}

func (w *windows) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	w.client = k8sClient
	w.k8sVersion = &k8sVersion
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
	nodeList := &v1.NodeList{}
	err := w.client.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return false
	}

	w.isWindowsNode = isWindowsNode(nodeList)
	// enable if windows node is detected
	// and k8s version of cluster is > 1.25.0
	// and CSI is enabled in STC
	return w.isWindowsNode && w.k8sVersion.GreaterThanOrEqual(k8s.K8sVer1_25) && pxutil.IsCSIEnabled(cluster)
}

func (w *windows) Reconcile(cluster *corev1.StorageCluster) error {

	var errList []string
	ownRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	if err := w.createDaemonSet(WindowsCsiDaemonsetFileName, WindowsCsiDaemonsetName, core.NamespaceSystem, cluster, nil); err != nil {
		logrus.Errorf("error during creating %s daemonset %s ", WindowsCsiDaemonsetName, err)
		errList = append(errList, err.Error())
	}

	if err := w.createDaemonSet(WindowsDriverDaemonsetFileName, WindowsDriverDaemonsetName, cluster.Namespace, cluster, ownRef); err != nil {
		logrus.Errorf("error during creating %s daemonset %s ", WindowsDriverDaemonsetName, err)
		errList = append(errList, err.Error())
	}

	if err := w.createStorageClass(); err != nil {
		logrus.Errorf("error during creating %s storageclass %s ", WindowsStorageClass, err)
		errList = append(errList, err.Error())
	}

	if len(errList) > 0 {
		return commonerrors.New(strings.Join(errList, " , "))
	}
	return nil
}

func (w *windows) Delete(cluster *corev1.StorageCluster) error {
	var errList []string

	if err := k8s.DeleteDaemonSet(w.client, WindowsCsiDaemonsetName, core.NamespaceSystem); err != nil {
		errList = append(errList, err.Error())
	}

	if err := k8s.DeleteDaemonSet(w.client, WindowsDriverDaemonsetName, cluster.Namespace); err != nil {
		errList = append(errList, err.Error())
	}

	if cluster.DeletionTimestamp != nil &&
		cluster.Spec.DeleteStrategy != nil && cluster.Spec.DeleteStrategy.Type != "" {
		if err := k8s.DeleteStorageClass(w.client, WindowsStorageClass); err != nil {
			errList = append(errList, err.Error())
		}
	}
	if len(errList) > 0 {
		return commonerrors.New(strings.Join(errList, " , "))
	}

	w.MarkDeleted()
	return nil
}

func (w *windows) MarkDeleted() {
	w.isCsiCreated = false
	w.isInstallerCreated = false
}

// RegisterPortworxPluginComponent registers the PortworxPlugin component
func RegisterWindowsComponent() {
	Register(WindowsComponentName, &windows{})
}

func init() {
	RegisterWindowsComponent()
}

func (w *windows) createDaemonSet(filename, daemonsetName, nameSpace string, cluster *corev1.StorageCluster, ownerRef *metav1.OwnerReference) error {

	daemonSet, err := k8s.GetDaemonSetFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}

	daemonSet.Name = daemonsetName
	daemonSet.Namespace = nameSpace
	if ownerRef != nil {
		daemonSet.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	}
	daemonSet.Spec.Template.ObjectMeta = k8s.AddManagedByOperatorLabel(daemonSet.Spec.Template.ObjectMeta)

	for i, container := range daemonSet.Spec.Template.Spec.Containers {
		var image string
		if container.Name == "node-driver-registrar" {
			image = w.getDesiredNodeRegistrarImage(cluster)
		}

		if container.Name == "liveness-probe" {
			image = w.getDesiredLivenessImage(cluster)
		}

		if container.Name == "windowsinstaller" {
			image = w.getDesiredInstallerImage(cluster)
		}

		daemonSet.Spec.Template.Spec.Containers[i].Image = image
	}

	existingDaemonSet := &appsv1.DaemonSet{}
	getErr := w.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      daemonsetName,
			Namespace: nameSpace,
		},
		existingDaemonSet,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &daemonSet.Spec.Template.Spec)
	pxutil.ApplyWindowsSettingsToPodSpec(&daemonSet.Spec.Template.Spec)

	equal, _ := util.DeepEqualPodTemplate(&daemonSet.Spec.Template, &existingDaemonSet.Spec.Template)
	if (!w.isCsiCreated && daemonSet.Name == WindowsCsiDaemonsetName) ||
		(!w.isInstallerCreated && daemonSet.Name == WindowsDriverDaemonsetName) ||
		!equal {
		if err := k8s.CreateOrUpdateDaemonSet(w.client, daemonSet, ownerRef); err != nil {
			return err
		}
	}

	if daemonSet.Name == WindowsCsiDaemonsetName {
		w.isCsiCreated = true
	}
	if daemonSet.Name == WindowsDriverDaemonsetName {
		w.isInstallerCreated = true
	}

	return nil
}

func (w *windows) getDesiredNodeRegistrarImage(cluster *corev1.StorageCluster) string {
	var imageName string

	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.CsiWindowsNodeRegistrar != "" {
		imageName = cluster.Status.DesiredImages.CsiWindowsNodeRegistrar
	}
	imageName = util.GetImageURN(cluster, imageName)
	return imageName
}

func (w *windows) getDesiredLivenessImage(cluster *corev1.StorageCluster) string {
	var imageName string
	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.CsiLivenessProbe != "" {
		imageName = cluster.Status.DesiredImages.CsiLivenessProbe
	}
	imageName = util.GetImageURN(cluster, imageName)
	return imageName
}

func (w *windows) getDesiredInstallerImage(cluster *corev1.StorageCluster) string {
	var imageName string
	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.CsiWindowsDriver != "" {
		imageName = cluster.Status.DesiredImages.CsiWindowsDriver
	}
	imageName = util.GetImageURN(cluster, imageName)
	return imageName
}

func (w *windows) createStorageClass() error {
	allowVolumeExpansion := true
	reclaimPolicy := v1.PersistentVolumeReclaimDelete
	bindingMode := storagev1.VolumeBindingImmediate

	return k8s.CreateStorageClass(
		w.client,
		&storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: WindowsStorageClass,
			},
			Parameters: map[string]string{
				"repl":     "2",
				"sharedv4": "true",
				"winshare": "true",
			},
			AllowVolumeExpansion: &allowVolumeExpansion,
			Provisioner:          "pxd.portworx.com",
			ReclaimPolicy:        &reclaimPolicy,
			VolumeBindingMode:    &bindingMode,
		},
	)
}

func isWindowsNode(nodeList *v1.NodeList) bool {
	// Check if the node has the label indicating it is running Windows
	for _, node := range nodeList.Items {
		nodeName := node.Name
		_, exists := node.Labels[v1.LabelOSStable]
		if exists && node.Labels[v1.LabelOSStable] == pxutil.WindowsOsName {
			logrus.Debugf("Node %s is running Windows\n", nodeName)
			return true
		} else {
			logrus.Debugf("Node %s is not running Windows\n", nodeName)
		}
	}
	return false
}
