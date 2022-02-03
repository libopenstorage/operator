package component

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// CSIComponentName name of the CSI component
	CSIComponentName = "CSI"
	// CSIServiceAccountName name of the CSI service account
	CSIServiceAccountName = "px-csi"
	// CSIClusterRoleName name of the CSI cluster role
	CSIClusterRoleName = "px-csi"
	// CSIClusterRoleBindingName name of the CSI cluster role binding
	CSIClusterRoleBindingName = "px-csi"
	// CSIServiceName name of the CSI service
	CSIServiceName = "px-csi-service"
	// CSIApplicationName name of the CSI application (deployment/statefulset)
	CSIApplicationName = "px-csi-ext"

	csiProvisionerContainerName = "csi-external-provisioner"
	csiAttacherContainerName    = "csi-attacher"
	csiSnapshotterContainerName = "csi-snapshotter"
	csiResizerContainerName     = "csi-resizer"
)

type csi struct {
	isCreated             bool
	csiNodeInfoCRDCreated bool
	k8sClient             client.Client
	k8sVersion            version.Version
}

func (c *csi) Name() string {
	return CSIComponentName
}

func (c *csi) Priority() int32 {
	return DefaultComponentPriority
}

func (c *csi) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.k8sVersion = k8sVersion
}

func (c *csi) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *csi) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsCSIEnabled(cluster)
}

func (c *csi) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	pxVersion := pxutil.GetPortworxVersion(cluster)
	csiConfig := c.getCSIConfiguration(cluster, pxVersion)

	if cluster.Status.DesiredImages == nil {
		cluster.Status.DesiredImages = &corev1.ComponentImages{}
	}

	if err := c.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := c.createClusterRole(cluster, csiConfig); err != nil {
		return err
	}
	if err := c.createClusterRoleBinding(cluster); err != nil {
		return err
	}
	if err := c.createService(cluster, ownerRef); err != nil {
		return err
	}
	if csiConfig.IncludeCsiDriverInfo {
		if err := c.createCSIDriver(csiConfig); err != nil {
			return err
		}
	}
	if csiConfig.UseDeployment {
		if err := k8sutil.DeleteStatefulSet(c.k8sClient, CSIApplicationName, cluster.Namespace, *ownerRef); err != nil {
			return err
		}
		if err := c.createDeployment(cluster, csiConfig, ownerRef); err != nil {
			return err
		}
	} else {
		if err := k8sutil.DeleteDeployment(c.k8sClient, CSIApplicationName, cluster.Namespace, *ownerRef); err != nil {
			return err
		}
		if err := c.createStatefulSet(cluster, csiConfig, ownerRef); err != nil {
			return err
		}
	}
	if csiConfig.CreateCsiNodeCrd && !c.csiNodeInfoCRDCreated {
		if err := createCSINodeInfoCRD(); err != nil {
			return err
		}
		c.csiNodeInfoCRDCreated = true
	}
	return nil
}

func (c *csi) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(c.k8sClient, CSIServiceAccountName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(c.k8sClient, CSIClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(c.k8sClient, CSIClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(c.k8sClient, CSIServiceName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteStatefulSet(c.k8sClient, CSIApplicationName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(c.k8sClient, CSIApplicationName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}

	pxVersion := pxutil.GetPortworxVersion(cluster)
	csiConfig := c.getCSIConfiguration(cluster, pxVersion)
	if csiConfig.IncludeCsiDriverInfo {
		if c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_18) {
			if err := k8sutil.DeleteCSIDriver(c.k8sClient, csiConfig.DriverName); err != nil {
				return err
			}
		} else {
			if err := k8sutil.DeleteCSIDriverBeta(c.k8sClient, csiConfig.DriverName); err != nil {
				return err
			}
		}

	}

	c.MarkDeleted()
	return nil
}

func (c *csi) MarkDeleted() {
	c.isCreated = false
	c.csiNodeInfoCRDCreated = false
}

func (c *csi) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		c.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CSIServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (c *csi) createClusterRole(
	cluster *corev1.StorageCluster,
	csiConfig *pxutil.CSIConfiguration,
) error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: CSIClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumes"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims/status"},
				Verbs:     []string{"update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"volumeattachments"},
				Verbs:     []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csistoragecapacities"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"list", "watch", "create", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{"snapshot.storage.k8s.io"},
				Resources: []string{
					"volumesnapshots",
					"volumesnapshotcontents",
					"volumesnapshotclasses",
					"volumesnapshots/status",
					"volumesnapshotcontents/status",
				},
				Verbs: []string{"get", "list", "watch", "create", "delete", "update", "patch"},
			},
			{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csidrivers"},
				Verbs:     []string{"create", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"endpoints"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"privileged"},
				Verbs:         []string{"use"},
			},
			{
				APIGroups:     []string{"policy"},
				Resources:     []string{"podsecuritypolicies"},
				ResourceNames: []string{constants.PrivilegedPSPName},
				Verbs:         []string{"use"},
			},
		},
	}

	k8sVer1_14, err := version.NewVersion("1.14")
	if err != nil {
		return err
	}

	if csiConfig.CreateCsiNodeCrd {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csinodeinfos"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	} else if c.k8sVersion.GreaterThan(k8sVer1_14) || c.k8sVersion.Equal(k8sVer1_14) {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csinodes"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	}

	if csiConfig.IncludeEndpointsAndConfigMapsForLeases {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
			},
		)
	}
	return k8sutil.CreateOrUpdateClusterRole(c.k8sClient, clusterRole)
}

func (c *csi) createClusterRoleBinding(
	cluster *corev1.StorageCluster,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		c.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: CSIClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      CSIServiceAccountName,
					Namespace: cluster.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     CSIClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func (c *csi) createService(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		c.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            CSIServiceName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				ClusterIP: "None",
			},
		},
		ownerRef,
	)
}

func (c *csi) createDeployment(
	cluster *corev1.StorageCluster,
	csiConfig *pxutil.CSIConfiguration,
	ownerRef *metav1.OwnerReference,
) error {
	existingDeployment := &appsv1.Deployment{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      CSIApplicationName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var (
		existingProvisionerImage = k8sutil.GetImageFromDeployment(existingDeployment, csiProvisionerContainerName)
		existingAttacherImage    = k8sutil.GetImageFromDeployment(existingDeployment, csiAttacherContainerName)
		existingSnapshotterImage = k8sutil.GetImageFromDeployment(existingDeployment, csiSnapshotterContainerName)
		existingResizerImage     = k8sutil.GetImageFromDeployment(existingDeployment, csiResizerContainerName)
		provisionerImage         string
		attacherImage            string
		snapshotterImage         string
		resizerImage             string
	)

	provisionerImage = util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.CSIProvisioner,
	)
	if csiConfig.IncludeAttacher && cluster.Status.DesiredImages.CSIAttacher != "" {
		attacherImage = util.GetImageURN(
			cluster,
			cluster.Status.DesiredImages.CSIAttacher,
		)
	}
	if csiConfig.IncludeSnapshotter && cluster.Status.DesiredImages.CSISnapshotter != "" {
		snapshotterImage = util.GetImageURN(
			cluster,
			cluster.Status.DesiredImages.CSISnapshotter,
		)
	}
	if csiConfig.IncludeResizer && cluster.Status.DesiredImages.CSIResizer != "" {
		resizerImage = util.GetImageURN(
			cluster,
			cluster.Status.DesiredImages.CSIResizer,
		)
	}

	modified := provisionerImage != existingProvisionerImage ||
		attacherImage != existingAttacherImage ||
		snapshotterImage != existingSnapshotterImage ||
		resizerImage != existingResizerImage ||
		util.HasPullSecretChanged(cluster, existingDeployment.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingDeployment.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingDeployment.Spec.Template.Spec.Tolerations)

	if !c.isCreated || modified {
		deployment := getCSIDeploymentSpec(cluster, csiConfig, ownerRef,
			provisionerImage, attacherImage, snapshotterImage, resizerImage)
		if err = k8sutil.CreateOrUpdateDeployment(c.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getCSIDeploymentSpec(
	cluster *corev1.StorageCluster,
	csiConfig *pxutil.CSIConfiguration,
	ownerRef *metav1.OwnerReference,
	provisionerImage, attacherImage string,
	snapshotterImage, resizerImage string,
) *appsv1.Deployment {
	replicas := int32(3)
	labels := map[string]string{
		"app": "px-csi-driver",
	}

	leaderElectionType := "leases"
	provisionerLeaderElectionType := "leases"
	if csiConfig.IncludeEndpointsAndConfigMapsForLeases {
		leaderElectionType = "configmaps"
		provisionerLeaderElectionType = "endpoints"
	}
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	var args []string
	if util.GetImageMajorVersion(provisionerImage) >= 2 {
		args = []string{
			"--v=3",
			"--csi-address=$(ADDRESS)",
			"--leader-election=true",
			"--default-fstype=ext4",
		}
	} else {
		args = []string{
			"--v=3",
			"--provisioner=" + csiConfig.DriverName,
			"--csi-address=$(ADDRESS)",
			"--enable-leader-election",
			"--leader-election-type=" + provisionerLeaderElectionType,
		}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            CSIApplicationName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: CSIServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            csiProvisionerContainerName,
							Image:           provisionerImage,
							ImagePullPolicy: imagePullPolicy,
							Args:            args,
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "socket-dir",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: csiConfig.DriverBasePath(),
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}

	if csiConfig.IncludeAttacher && attacherImage != "" {
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiAttacherContainerName,
				Image:           attacherImage,
				ImagePullPolicy: imagePullPolicy,
				Args: []string{
					"--v=3",
					"--csi-address=$(ADDRESS)",
					"--leader-election=true",
					"--leader-election-type=" + leaderElectionType,
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/csi",
					},
				},
			},
		)
	}

	if csiConfig.IncludeSnapshotter && snapshotterImage != "" {
		snapshotterContainer := v1.Container{
			Name:            csiSnapshotterContainerName,
			Image:           snapshotterImage,
			ImagePullPolicy: imagePullPolicy,
			Args: []string{
				"--v=3",
				"--csi-address=$(ADDRESS)",
				"--leader-election=true",
			},
			Env: []v1.EnvVar{
				{
					Name:  "ADDRESS",
					Value: "/csi/csi.sock",
				},
			},
			VolumeMounts: []v1.VolumeMount{
				{
					Name:      "socket-dir",
					MountPath: "/csi",
				},
			},
		}
		if csiConfig.IncludeEndpointsAndConfigMapsForLeases {
			snapshotterContainer.Args = append(
				snapshotterContainer.Args,
				"--leader-election-type=configmaps",
			)
		}
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			snapshotterContainer,
		)
	}

	if csiConfig.IncludeResizer && resizerImage != "" {
		deployment.Spec.Template.Spec.Containers = append(
			deployment.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiResizerContainerName,
				Image:           resizerImage,
				ImagePullPolicy: imagePullPolicy,
				Args: []string{
					"--v=3",
					"--csi-address=$(ADDRESS)",
					"--leader-election=true",
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "socket-dir",
						MountPath: "/csi",
					},
				},
			},
		)
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		deployment.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			deployment.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			deployment.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				deployment.Spec.Template.Spec.Tolerations = append(
					deployment.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return deployment
}

func (c *csi) createStatefulSet(
	cluster *corev1.StorageCluster,
	csiConfig *pxutil.CSIConfiguration,
	ownerRef *metav1.OwnerReference,
) error {
	existingSS := &appsv1.StatefulSet{}
	err := c.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      CSIApplicationName,
			Namespace: cluster.Namespace,
		},
		existingSS,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var (
		existingProvisionerImage = getImageFromStatefulSet(existingSS, csiProvisionerContainerName)
		existingAttacherImage    = getImageFromStatefulSet(existingSS, csiAttacherContainerName)
		provisionerImage         string
		attacherImage            string
	)

	provisionerImage = util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.CSIProvisioner,
	)
	attacherImage = util.GetImageURN(
		cluster,
		cluster.Status.DesiredImages.CSIAttacher,
	)

	modified := provisionerImage != existingProvisionerImage ||
		attacherImage != existingAttacherImage ||
		util.HasPullSecretChanged(cluster, existingSS.Spec.Template.Spec.ImagePullSecrets) ||
		util.HasNodeAffinityChanged(cluster, existingSS.Spec.Template.Spec.Affinity) ||
		util.HaveTolerationsChanged(cluster, existingSS.Spec.Template.Spec.Tolerations)

	if !c.isCreated || modified {
		statefulSet := getCSIStatefulSetSpec(cluster, csiConfig, ownerRef, provisionerImage, attacherImage)
		if err = k8sutil.CreateOrUpdateStatefulSet(c.k8sClient, statefulSet, ownerRef); err != nil {
			return err
		}
	}
	c.isCreated = true
	return nil
}

func getCSIStatefulSetSpec(
	cluster *corev1.StorageCluster,
	csiConfig *pxutil.CSIConfiguration,
	ownerRef *metav1.OwnerReference,
	provisionerImage, attacherImage string,
) *appsv1.StatefulSet {
	replicas := int32(1)
	labels := map[string]string{
		"app": "px-csi-driver",
	}
	imagePullPolicy := pxutil.ImagePullPolicy(cluster)

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            CSIApplicationName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: CSIServiceName,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: CSIServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            csiProvisionerContainerName,
							Image:           provisionerImage,
							ImagePullPolicy: imagePullPolicy,
							Args: []string{
								"--v=3",
								"--provisioner=" + csiConfig.DriverName,
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
						{
							Name:            csiAttacherContainerName,
							Image:           attacherImage,
							ImagePullPolicy: imagePullPolicy,
							Args: []string{
								"--v=3",
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "socket-dir",
									MountPath: "/csi",
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "socket-dir",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: csiConfig.DriverBasePath(),
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}

	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		statefulSet.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *cluster.Spec.ImagePullSecret,
			},
		)
	}

	if cluster.Spec.Placement != nil {
		if cluster.Spec.Placement.NodeAffinity != nil {
			statefulSet.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(cluster.Spec.Placement.Tolerations) > 0 {
			statefulSet.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range cluster.Spec.Placement.Tolerations {
				statefulSet.Spec.Template.Spec.Tolerations = append(
					statefulSet.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return statefulSet
}

func (c *csi) createCSIDriver(
	csiConfig *pxutil.CSIConfiguration,
) error {
	// For k8s 1.18 and later, use the GA CSI Driver
	if c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_18) {
		volumeLifecycleModes := []storagev1.VolumeLifecycleMode{
			storagev1.VolumeLifecyclePersistent,
		}
		if csiConfig.IncludeEphemeralSupport {
			volumeLifecycleModes = append(volumeLifecycleModes, storagev1.VolumeLifecycleEphemeral)
		}

		csiDriver := &storagev1.CSIDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name: csiConfig.DriverName,
			},
			Spec: storagev1.CSIDriverSpec{
				AttachRequired:       boolPtr(false),
				PodInfoOnMount:       boolPtr(true),
				VolumeLifecycleModes: volumeLifecycleModes,
			},
		}

		// This field is beta in Kubernetes 1.20 and GA in Kubernetes 1.23
		if c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_20) {
			fsGroupPolicy := storagev1.FileFSGroupPolicy
			csiDriver.Spec.FSGroupPolicy = &fsGroupPolicy
		}

		return k8sutil.CreateOrUpdateCSIDriver(c.k8sClient, csiDriver)

	}

	volumeLifecycleModes := []storagev1beta1.VolumeLifecycleMode{
		storagev1beta1.VolumeLifecyclePersistent,
	}
	if csiConfig.IncludeEphemeralSupport {
		volumeLifecycleModes = append(volumeLifecycleModes, storagev1beta1.VolumeLifecycleEphemeral)
	}

	return k8sutil.CreateOrUpdateCSIDriverBeta(
		c.k8sClient,
		&storagev1beta1.CSIDriver{
			ObjectMeta: metav1.ObjectMeta{
				Name: csiConfig.DriverName,
			},
			Spec: storagev1beta1.CSIDriverSpec{
				AttachRequired:       boolPtr(false),
				PodInfoOnMount:       boolPtr(true),
				VolumeLifecycleModes: volumeLifecycleModes,
			},
		},
	)
}

func createCSINodeInfoCRD() error {
	logrus.Debugf("Creating CSINodeInfo CRD")

	resource := apiextensionsops.CustomResource{
		Plural: "csinodeinfos",
		Group:  "csi.storage.k8s.io",
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource.Plural, resource.Group),
			Labels: map[string]string{
				"addonmanager.kubernetes.io/mode": "Reconcile",
			},
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   resource.Group,
			Version: "v1alpha1",
			Scope:   apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: resource.Plural,
				Kind:   "CSINodeInfo",
			},
			Validation: &apiextensionsv1beta1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"spec": {
							Description: "Specification of CSINodeInfo",
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"drivers": {
									Description: "List of CSI drivers running on the node and their specs.",
									Type:        "array",
									Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
										Schema: &apiextensionsv1beta1.JSONSchemaProps{
											Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
												"name": {
													Description: "The CSI driver that this object refers to.",
													Type:        "string",
												},
												"nodeID": {
													Description: "The node from the driver point of view.",
													Type:        "string",
												},
												"topologyKeys": {
													Description: "List of keys supported by the driver.",
													Type:        "array",
													Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
														Schema: &apiextensionsv1beta1.JSONSchemaProps{
															Type: "string",
														},
													},
												},
											},
										},
									},
								},
							},
						},
						"status": {
							Description: "Status of CSINodeInfo",
							Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
								"drivers": {
									Description: "List of CSI drivers running on the node and their statuses.",
									Type:        "array",
									Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
										Schema: &apiextensionsv1beta1.JSONSchemaProps{
											Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
												"name": {
													Description: "The CSI driver that this object refers to.",
													Type:        "string",
												},
												"available": {
													Description: "Whether the CSI driver is installed.",
													Type:        "boolean",
												},
												"volumePluginMechanism": {
													Description: "Indicates to external components the required mechanism " +
														"to use for any in-tree plugins replaced by this driver.",
													Type:    "string",
													Pattern: "in-tree|csi",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := apiextensionsops.Instance().RegisterCRDV1beta1(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return apiextensionsops.Instance().ValidateCRDV1beta1(resource, 1*time.Minute, 5*time.Second)
}

func (c *csi) getCSIConfiguration(
	cluster *corev1.StorageCluster,
	pxVersion *version.Version,
) *pxutil.CSIConfiguration {
	deprecatedCSIDriverName := pxutil.UseDeprecatedCSIDriverName(cluster)
	disableCSIAlpha := pxutil.DisableCSIAlpha(cluster)
	kubeletPath := pxutil.KubeletPath(cluster)
	csiGenerator := pxutil.NewCSIGenerator(c.k8sVersion, *pxVersion,
		deprecatedCSIDriverName, disableCSIAlpha, kubeletPath)
	if pxutil.IsCSIEnabled(cluster) {
		return csiGenerator.GetCSIConfiguration()
	}
	return csiGenerator.GetBasicCSIConfiguration()
}

func getImageFromStatefulSet(ss *appsv1.StatefulSet, containerName string) string {
	for _, c := range ss.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

func boolPtr(val bool) *bool {
	return &val
}

func hostPathTypePtr(val v1.HostPathType) *v1.HostPathType {
	return &val
}

// RegisterCSIComponent registers the CSI component
func RegisterCSIComponent() {
	Register(CSIComponentName, &csi{})
}

func init() {
	RegisterCSIComponent()
}
