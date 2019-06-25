package portworx

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"time"

	version "github.com/hashicorp/go-version"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxServiceAccountName             = "portworx"
	pxClusterRoleName                = "portworx"
	pxClusterRoleBindingName         = "portworx"
	pxRoleName                       = "portworx"
	pxRoleBindingName                = "portworx"
	pxServiceName                    = "portworx-service"
	pxAPIServiceName                 = "portworx-api"
	pxAPIDaemonSetName               = "portworx-api"
	pvcServiceAccountName            = "portworx-pvc-controller"
	pvcClusterRoleName               = "portworx-pvc-controller"
	pvcClusterRoleBindingName        = "portworx-pvc-controller"
	pvcDeploymentName                = "portworx-pvc-controller"
	pvcContainerName                 = "portworx-pvc-controller-manager"
	lhServiceAccountName             = "px-lighthouse"
	lhClusterRoleName                = "px-lighthouse"
	lhClusterRoleBindingName         = "px-lighthouse"
	lhServiceName                    = "px-lighthouse"
	lhDeploymentName                 = "px-lighthouse"
	lhContainerName                  = "px-lighthouse"
	csiServiceAccountName            = "px-csi"
	csiClusterRoleName               = "px-csi"
	csiClusterRoleBindingName        = "px-csi"
	csiServiceName                   = "px-csi-service"
	csiStatefulSetName               = "px-csi-ext"
	csiProvisionerContainerName      = "csi-external-provisioner"
	csiAttacherContainerName         = "csi-attacher"
	csiClusterRegistrarContainerName = "csi-cluster-registrar"
	csiSnapshotterContainerName      = "csi-snapshotter"
	pxRESTPortName                   = "px-api"
)

const (
	defaultPVCControllerCPU = "200m"
	envKeyPortworxNamespace = "PX_NAMESPACE"
)

var (
	kbVerRegex     = regexp.MustCompile(`^(v\d+\.\d+\.\d+).*`)
	controllerKind = corev1alpha1.SchemeGroupVersion.WithKind("StorageCluster")
)

func (p *portworx) installComponents(cluster *corev1alpha1.StorageCluster) error {
	t, err := newTemplate(cluster)
	if err != nil {
		return err
	}

	if err = p.setupPortworxRBAC(t.cluster); err != nil {
		return err
	}
	if err = p.setupPortworxService(t); err != nil {
		return err
	}
	if err = p.setupPortworxAPI(t); err != nil {
		return err
	}
	if err = p.createCustomResourceDefinitions(); err != nil {
		return err
	}

	if t.needsPVCController {
		if err = p.setupPVCController(t); err != nil {
			return err
		}
	} else {
		if err = p.removePVCController(t.cluster.Namespace); err != nil {
			return err
		}
	}

	if cluster.Spec.UserInterface != nil && cluster.Spec.UserInterface.Enabled {
		if err = p.setupLighthouse(t); err != nil {
			return err
		}
	} else {
		if err = p.removeLighthouse(t.cluster.Namespace); err != nil {
			return err
		}
	}

	if FeatureCSI.isEnabled(cluster.Spec.FeatureGates) {
		if err = p.setupCSI(t); err != nil {
			return err
		}
	} else {
		if err = p.removeCSI(t.cluster.Namespace); err != nil {
			return err
		}
	}

	return nil
}

func (p *portworx) setupPortworxRBAC(cluster *corev1alpha1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, controllerKind)
	if err := p.createServiceAccount(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createClusterRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createRole(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createRoleBinding(cluster.Namespace, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupPortworxService(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	return p.createPortworxService(t, ownerRef)
}

func (p *portworx) setupPortworxAPI(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createPortworxAPIService(t, ownerRef); err != nil {
		return err
	}
	if !p.pxAPIDaemonSetCreated {
		if err := p.createPortworxAPIDaemonSet(t, ownerRef); err != nil {
			return err
		}
		p.pxAPIDaemonSetCreated = true
	}
	return nil
}

func (p *portworx) setupPVCController(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createPVCControllerServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createPVCControllerDeployment(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupLighthouse(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createLighthouseServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseClusterRole(ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseService(t, ownerRef); err != nil {
		return err
	}
	if err := p.createLighthouseDeployment(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) setupCSI(t *template) error {
	ownerRef := metav1.NewControllerRef(t.cluster, controllerKind)
	if err := p.createCSIServiceAccount(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIClusterRole(t, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIClusterRoleBinding(t.cluster.Namespace, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIService(t, ownerRef); err != nil {
		return err
	}
	if err := p.createCSIStatefulSet(t, ownerRef); err != nil {
		return err
	}
	return nil
}

func (p *portworx) createCustomResourceDefinitions() error {
	if !p.volumePlacementStrategyCRDCreated {
		if err := createVolumePlacementStrategyCRD(); err != nil {
			return err
		}
		p.volumePlacementStrategyCRDCreated = true
	}
	return nil
}

func (p *portworx) removePVCController(namespace string) error {
	// We don't delete the service account for PVC controller because it is part of CSV. If
	// we disable PVC controller then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, pvcClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, pvcClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, pvcDeploymentName, namespace); err != nil {
		return err
	}
	p.pvcControllerDeploymentCreated = false
	return nil
}

func (p *portworx) removeLighthouse(namespace string) error {
	// We don't delete the service account for Lighthouse because it is part of CSV. If
	// we disable Lighthouse then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, lhClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, lhClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(p.k8sClient, lhServiceName, namespace); err != nil {
		return err
	}
	if err := k8sutil.DeleteDeployment(p.k8sClient, lhDeploymentName, namespace); err != nil {
		return err
	}
	p.lhDeploymentCreated = false
	return nil
}

func (p *portworx) removeCSI(namespace string) error {
	// We don't delete the service account for CSI because it is part of CSV. If
	// we disable CSI then the CSV upgrades would fail as requirements are not met.
	if err := k8sutil.DeleteClusterRole(p.k8sClient, csiClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(p.k8sClient, csiClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteService(p.k8sClient, csiServiceName, namespace); err != nil {
		return err
	}
	if err := k8sutil.DeleteStatefulSet(p.k8sClient, csiStatefulSetName, namespace); err != nil {
		return err
	}
	p.csiStatefulSetCreated = false
	return nil
}

func (p *portworx) unsetInstallParams() {
	p.pxAPIDaemonSetCreated = false
	p.volumePlacementStrategyCRDCreated = false
	p.pvcControllerDeploymentCreated = false
	p.lhDeploymentCreated = false
	p.csiStatefulSetCreated = false
}

func createVolumePlacementStrategyCRD() error {
	logrus.Debugf("Creating VolumePlacementStrategy CRD")
	resource := k8s.CustomResource{
		Name:       "volumeplacementstrategy",
		Plural:     "volumeplacementstrategies",
		Group:      "portworx.io",
		Version:    "v1beta1",
		Scope:      apiextensionsv1beta1.ClusterScoped,
		Kind:       "VolumePlacementStrategy",
		ShortNames: []string{"vps", "vp"},
	}
	err := k8s.Instance().CreateCRD(resource)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return k8s.Instance().ValidateCRD(resource, 1*time.Minute, 5*time.Second)
}

func (p *portworx) createServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIServiceAccount(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		p.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiServiceAccountName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createRole(clusterNamespace string, ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateRole(
		p.k8sClient,
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list", "create", "update", "patch"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateRoleBinding(
		p.k8sClient,
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxRoleBindingName,
				Namespace:       clusterNamespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				Name:     pxRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "create", "update"},
				},
				{
					APIGroups:     []string{"extensions"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{"privileged"},
					Verbs:         []string{"use"},
				},
				{
					APIGroups: []string{"portworx.io"},
					Resources: []string{"volumeplacementstrategies"},
					Verbs:     []string{"get", "list"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes"},
					Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumes/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims"},
					Verbs:     []string{"get", "list", "watch", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims/status"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch", "create", "delete"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints", "services"},
					Verbs:     []string{"get", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"events"},
					Verbs:     []string{"watch", "create", "update", "patch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"serviceaccounts"},
					Verbs:     []string{"get", "create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseClusterRole(ownerRef *metav1.OwnerReference) error {
	return k8sutil.CreateOrUpdateClusterRole(
		p.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"nodes", "services"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"*"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIClusterRole(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:            csiClusterRoleName,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"extensions"},
				Resources:     []string{"podsecuritypolicies"},
				ResourceNames: []string{"privileged"},
				Verbs:         []string{"use"},
			},
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
				Verbs:     []string{"get", "list", "watch", "create", "delete", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"volumeattachments"},
				Verbs:     []string{"get", "list", "watch", "update"},
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
				Resources: []string{"volumesnapshots", "volumesnapshotcontents"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csidrivers"},
				Verbs:     []string{"create", "delete"},
			},
		},
	}

	k8sVer1_14, err := version.NewVersion("1.14")
	if err != nil {
		return err
	}

	if t.csiVersions.createCsiNodeCrd {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"csi.storage.k8s.io"},
				Resources: []string{"csinodeinfos"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	} else if t.k8sVersion.GreaterThan(k8sVer1_14) || t.k8sVersion.Equal(k8sVer1_14) {
		clusterRole.Rules = append(
			clusterRole.Rules,
			rbacv1.PolicyRule{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"csinodes"},
				Verbs:     []string{"get", "list", "watch", "update"},
			},
		)
	}
	return k8sutil.CreateOrUpdateClusterRole(p.k8sClient, clusterRole, ownerRef)
}

func (p *portworx) createClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pxClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pvcClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pvcServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pvcClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createLighthouseClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            lhClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      lhServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     lhClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createCSIClusterRoleBinding(
	clusterNamespace string,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		p.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiClusterRoleBindingName,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      csiServiceAccountName,
					Namespace: clusterNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     csiClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPortworxService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := p.GetSelectorLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       pxRESTPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(t.startPort),
				},
				{
					Name:       "px-kvdb",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9019),
					TargetPort: intstr.FromInt(t.startPort + 18),
				},
				{
					Name:       "px-sdk",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9020),
					TargetPort: intstr.FromInt(t.startPort + 19),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(t.startPort + 20),
				},
			},
		},
	}
	if !t.isAKS && !t.isGKE && !t.isEKS {
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createPortworxAPIService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getPortworxAPIServiceLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       pxRESTPortName,
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9001),
					TargetPort: intstr.FromInt(t.startPort),
				},
				{
					Name:       "px-sdk",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9020),
					TargetPort: intstr.FromInt(t.startPort + 19),
				},
				{
					Name:       "px-rest-gateway",
					Protocol:   v1.ProtocolTCP,
					Port:       int32(9021),
					TargetPort: intstr.FromInt(t.startPort + 20),
				},
			},
		},
	}
	if !t.isAKS && !t.isGKE && !t.isEKS {
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createLighthouseService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	labels := getLighthouseLabels()

	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhServiceName,
			Namespace:       t.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Type:     v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       int32(80),
					TargetPort: intstr.FromInt(80),
				},
				{
					Name:       "https",
					Port:       int32(443),
					TargetPort: intstr.FromInt(443),
				},
			},
		},
	}
	if !t.isAKS && !t.isGKE && !t.isEKS {
		newService.Spec.Type = v1.ServiceTypeNodePort
	}

	return k8sutil.CreateOrUpdateService(p.k8sClient, newService, ownerRef)
}

func (p *portworx) createCSIService(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateService(
		p.k8sClient,
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            csiServiceName,
				Namespace:       t.cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: v1.ServiceSpec{
				ClusterIP: "None",
			},
		},
		ownerRef,
	)
}

func (p *portworx) createPVCControllerDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	targetCPU := defaultPVCControllerCPU
	if cpuStr, ok := t.cluster.Annotations[annotationPVCControllerCPU]; ok {
		targetCPU = cpuStr
	}
	targetCPUQuantity, err := resource.ParseQuantity(targetCPU)
	if err != nil {
		return err
	}

	imageName := util.GetImageURN(t.cluster.Spec.CustomImageRegistry,
		"gcr.io/google_containers/kube-controller-manager-amd64:v"+t.k8sVersion.String())

	command := []string{
		"kube-controller-manager",
		"--leader-elect=true",
		"--address=0.0.0.0",
		"--controllers=persistentvolume-binder,persistentvolume-expander",
		"--use-service-account-credentials=true",
	}
	if t.isOpenshift {
		command = append(command, "--leader-elect-resource-lock=endpoints")
	} else {
		command = append(command, "--leader-elect-resource-lock=configmaps")
	}

	existingDeployment := &appsv1.Deployment{}
	err = p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pvcDeploymentName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var existingImage string
	var existingCommand []string
	var existingCPUQuantity resource.Quantity
	for _, c := range existingDeployment.Spec.Template.Spec.Containers {
		if c.Name == pvcContainerName {
			existingImage = c.Image
			existingCommand = c.Command
			existingCPUQuantity = c.Resources.Requests[v1.ResourceCPU]
		}
	}

	modified := existingImage != imageName ||
		!reflect.DeepEqual(existingCommand, command) ||
		existingCPUQuantity.Cmp(targetCPUQuantity) != 0

	if !p.pvcControllerDeploymentCreated || modified {
		deployment := getPVCControllerDeploymentSpec(t, ownerRef, imageName, command, targetCPUQuantity)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.pvcControllerDeploymentCreated = true
	return nil
}

func getPVCControllerDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	imageName string,
	command []string,
	cpuQuantity resource.Quantity,
) *appsv1.Deployment {
	replicas := int32(3)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	labels := map[string]string{
		"name": pvcDeploymentName,
		"tier": "control-plane",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pvcDeploymentName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels: map[string]string{
				"tier": "control-plane",
			},
			Annotations: map[string]string{
				"scheduler.alpha.kubernetes.io/critical-pod": "",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"scheduler.alpha.kubernetes.io/critical-pod": "",
					},
				},
				Spec: v1.PodSpec{
					ServiceAccountName: pvcServiceAccountName,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:    pvcContainerName,
							Image:   imageName,
							Command: command,
							LivenessProbe: &v1.Probe{
								FailureThreshold:    8,
								TimeoutSeconds:      15,
								InitialDelaySeconds: 15,
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Host:   "127.0.0.1",
										Path:   "/healthz",
										Port:   intstr.FromInt(10252),
										Scheme: v1.URISchemeHTTP,
									},
								},
							},
							Resources: v1.ResourceRequirements{
								Requests: map[v1.ResourceName]resource.Quantity{
									v1.ResourceCPU: cpuQuantity,
								},
							},
						},
					},
					Affinity: &v1.Affinity{
						PodAntiAffinity: &v1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
								{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "name",
												Operator: metav1.LabelSelectorOpIn,
												Values: []string{
													pvcDeploymentName,
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
}

func (p *portworx) createLighthouseDeployment(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	if t.cluster.Spec.UserInterface.Image == "" {
		return fmt.Errorf("lighthouse image cannot be empty")
	}

	existingDeployment := &appsv1.Deployment{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      lhDeploymentName,
			Namespace: t.cluster.Namespace,
		},
		existingDeployment,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	existingImage := getLighthouseImage(existingDeployment)
	lhImageName := util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		t.cluster.Spec.UserInterface.Image,
	)

	if !p.lhDeploymentCreated || lhImageName != existingImage {
		deployment := getLighthouseDeploymentSpec(t, ownerRef, lhImageName)
		if err = k8sutil.CreateOrUpdateDeployment(p.k8sClient, deployment, ownerRef); err != nil {
			return err
		}
	}
	p.lhDeploymentCreated = true
	return nil
}

func getLighthouseDeploymentSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	lhImageName string,
) *appsv1.Deployment {
	labels := getLighthouseLabels()
	replicas := int32(1)
	maxUnavailable := intstr.FromInt(1)
	maxSurge := intstr.FromInt(1)

	imageRegistry := t.cluster.Spec.CustomImageRegistry
	// TODO: We should probably map these to the lighthouse version
	configSyncImageName := util.GetImageURN(imageRegistry, "portworx/lh-config-sync:0.3")
	storkConnectorImageName := util.GetImageURN(imageRegistry, "portworx/lh-stork-connector:0.1")

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            lhDeploymentName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: lhServiceAccountName,
					InitContainers: []v1.Container{
						{
							Name:            "config-init",
							Image:           configSyncImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"init"},
							Env: []v1.EnvVar{
								{
									Name:  envKeyPortworxNamespace,
									Value: t.cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:            lhContainerName,
							Image:           lhImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"-kubernetes", "true"},
							Ports: []v1.ContainerPort{
								{
									ContainerPort: int32(80),
								},
								{
									ContainerPort: int32(443),
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            "config-sync",
							Image:           configSyncImageName,
							ImagePullPolicy: t.imagePullPolicy,
							Args:            []string{"sync"},
							Env: []v1.EnvVar{
								{
									Name:  envKeyPortworxNamespace,
									Value: t.cluster.Namespace,
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/config/lh",
								},
							},
						},
						{
							Name:            "stork-connector",
							Image:           storkConnectorImageName,
							ImagePullPolicy: t.imagePullPolicy,
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "config",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}
}

func (p *portworx) createCSIStatefulSet(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	existingSS := &appsv1.StatefulSet{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      csiStatefulSetName,
			Namespace: t.cluster.Namespace,
		},
		existingSS,
	)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	var (
		existingProvisionerImage = getImageFromStatefulSet(existingSS, csiProvisionerContainerName)
		existingAttacherImage    = getImageFromStatefulSet(existingSS, csiAttacherContainerName)
		existingRegistrarImage   = getImageFromStatefulSet(existingSS, csiClusterRegistrarContainerName)
		existingSnapshotterImage = getImageFromStatefulSet(existingSS, csiSnapshotterContainerName)
		provisionerImage         string
		attacherImage            string
		registrarImage           string
		snapshotterImage         string
	)

	provisionerImage = util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		"quay.io/k8scsi/csi-provisioner:v"+t.csiVersions.provisionerImage,
	)
	attacherImage = util.GetImageURN(
		t.cluster.Spec.CustomImageRegistry,
		"quay.io/k8scsi/csi-attacher:v"+t.csiVersions.attacher,
	)
	if t.csiVersions.clusterRegistrar != "" {
		registrarImage = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			"quay.io/k8scsi/csi-cluster-driver-registrar:v"+t.csiVersions.clusterRegistrar,
		)
	}
	if t.csiVersions.snapshotter != "" {
		snapshotterImage = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			"quay.io/k8scsi/csi-snapshotter:v"+t.csiVersions.snapshotter,
		)
	}

	if !p.csiStatefulSetCreated ||
		provisionerImage != existingProvisionerImage ||
		attacherImage != existingAttacherImage ||
		registrarImage != existingRegistrarImage ||
		snapshotterImage != existingSnapshotterImage {
		statefulSet := getCSIStatefulSetSpec(t, ownerRef,
			provisionerImage, attacherImage, registrarImage, snapshotterImage)
		if err = k8sutil.CreateOrUpdateStatefulSet(p.k8sClient, statefulSet, ownerRef); err != nil {
			return err
		}
	}
	p.csiStatefulSetCreated = true
	return nil
}

func getCSIStatefulSetSpec(
	t *template,
	ownerRef *metav1.OwnerReference,
	provisionerImage, attacherImage string,
	registrarImage, snapshotterImage string,
) *appsv1.StatefulSet {
	replicas := int32(1)
	labels := map[string]string{
		"app": "px-csi-driver",
	}

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            csiStatefulSetName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: csiServiceName,
			Replicas:    &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					ServiceAccountName: csiServiceAccountName,
					Containers: []v1.Container{
						{
							Name:            csiProvisionerContainerName,
							Image:           provisionerImage,
							ImagePullPolicy: t.imagePullPolicy,
							Args: []string{
								"--v=5",
								"--provisioner=com.openstorage.pxd",
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: boolPtr(true),
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
							ImagePullPolicy: t.imagePullPolicy,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
							},
							Env: []v1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							SecurityContext: &v1.SecurityContext{
								Privileged: boolPtr(true),
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
									Path: "/var/lib/kubelet/plugins/com.openstorage.pxd",
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
					},
				},
			},
		},
	}

	if registrarImage != "" {
		statefulSet.Spec.Template.Spec.Containers = append(
			statefulSet.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiClusterRegistrarContainerName,
				Image:           registrarImage,
				ImagePullPolicy: t.imagePullPolicy,
				Args: []string{
					"--v=5",
					"--csi-address=$(ADDRESS)",
					"--pod-info-mount-version=v1",
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: boolPtr(true),
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

	if snapshotterImage != "" {
		statefulSet.Spec.Template.Spec.Containers = append(
			statefulSet.Spec.Template.Spec.Containers,
			v1.Container{
				Name:            csiSnapshotterContainerName,
				Image:           snapshotterImage,
				ImagePullPolicy: t.imagePullPolicy,
				Args: []string{
					"--v=5",
					"--csi-address=$(ADDRESS)",
				},
				Env: []v1.EnvVar{
					{
						Name:  "ADDRESS",
						Value: "/csi/csi.sock",
					},
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: boolPtr(true),
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

	return statefulSet
}

func (p *portworx) createPortworxAPIDaemonSet(
	t *template,
	ownerRef *metav1.OwnerReference,
) error {
	imageName := util.GetImageURN(t.cluster.Spec.CustomImageRegistry, "k8s.gcr.io/pause:3.1")

	maxUnavailable := intstr.FromString("100%")
	newDaemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxAPIDaemonSetName,
			Namespace:       t.cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: getPortworxAPIServiceLabels(),
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: getPortworxAPIServiceLabels(),
				},
				Spec: v1.PodSpec{
					ServiceAccountName: pxServiceAccountName,
					RestartPolicy:      v1.RestartPolicyAlways,
					HostNetwork:        true,
					Containers: []v1.Container{
						{
							Name:            "portworx-api",
							Image:           imageName,
							ImagePullPolicy: v1.PullAlways,
							ReadinessProbe: &v1.Probe{
								PeriodSeconds: int32(10),
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Host: "127.0.0.1",
										Path: "/status",
										Port: intstr.FromInt(t.startPort),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if t.cluster.Spec.Placement != nil && t.cluster.Spec.Placement.NodeAffinity != nil {
		newDaemonSet.Spec.Template.Spec.Affinity = &v1.Affinity{
			NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	return k8sutil.CreateOrUpdateDaemonSet(p.k8sClient, newDaemonSet, ownerRef)
}

func getPortworxAPIServiceLabels() map[string]string {
	return map[string]string{
		"name": pxAPIServiceName,
	}
}

func getLighthouseLabels() map[string]string {
	return map[string]string{
		"tier": "px-web-console",
	}
}

func getLighthouseImage(deployment *appsv1.Deployment) string {
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == lhContainerName {
			return c.Image
		}
	}
	return ""
}

func getImageFromStatefulSet(ss *appsv1.StatefulSet, containerName string) string {
	for _, c := range ss.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}
