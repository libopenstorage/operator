package portworx

import (
	"context"
	"path"
	"strconv"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

// PreFlightPortworx provides a set of APIs to uninstall portworx
type PreFlightPortworx interface {
	// RunPreFlight runs the node-wiper daemonset
	RunPreFlight(removeData bool, recorder record.EventRecorder) error
	// GetPreFlightStatus returns the status of the node-wiper daemonset
	// returns the no. of completed, in progress and total pods
	GetPreFlightStatus() (int32, int32, int32, error)
	// DeletePreFlight deletes the node-wiper daemonset
	DeletePreFlight() error
}

// NewPreFlighter returns an implementation of PreFlightPortworx interface
func NewPreFlighter(
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
) PreFlightPortworx {
	return &preFlightPortworx{
		cluster:   cluster,
		k8sClient: k8sClient,
	}
}

type preFlightPortworx struct {
	cluster   *corev1.StorageCluster
	k8sClient client.Client
}

func (u *preFlightPortworx) GetPreFlightStatus() (int32, int32, int32, error) {
	ds := &appsv1.DaemonSet{}
	err := u.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pxPreFlightDaemonSetName,
			Namespace: u.cluster.Namespace,
		},
		ds,
	)
	if err != nil {
		return -1, -1, -1, err
	}

	pods, err := k8sutil.GetDaemonSetPods(u.k8sClient, ds)
	if err != nil {
		return -1, -1, -1, err
	}
	totalPods := ds.Status.DesiredNumberScheduled
	completedPods := 0
	for _, pod := range pods {
		if len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].Ready {
			completedPods++
		}
	}
	logrus.Infof("Node Wiper Status: Completed [%v] InProgress [%v] Total Pods [%v]", completedPods, totalPods-int32(completedPods), totalPods)
	return int32(completedPods), totalPods - int32(completedPods), totalPods, nil
}

func (u *preFlightPortworx) RunPreFlight(
	removeData bool,
	recorder record.EventRecorder,
) error {
	pwxHostPathRoot := "/"
	coresPwx := varCores

	enabled, err := strconv.ParseBool(u.cluster.Annotations[pxutil.AnnotationIsPKS])
	isPKS := err == nil && enabled

	if isPKS {
		pwxHostPathRoot = pksPersistentStoreRoot
		coresPwx = path.Join(pwxHostPathRoot, path.Base(varCores))
	}

	trueVar := true
	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	wiperImage := k8sutil.GetValueFromEnv(envKeyPreFlightImage, u.cluster.Spec.Env)
	if len(wiperImage) == 0 {
		release := manifest.Instance().GetVersions(u.cluster, true)
		wiperImage = release.Components.PreFlight
	}
	wiperImage = util.GetImageURN(u.cluster, wiperImage)

	args := []string{"-w"}
	if removeData {
		args = append(args, "-r")
	}

	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())

	err = u.createServiceAccount(ownerRef)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	err = u.createClusterRole()
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	err = u.createClusterRoleBinding()
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	podSpec, err := p.GetStoragePodSpec(cluster, "")
	if err != nil {
		return err
	}

	// Create daemonset from podSpec
	labels := map[string]string{
		"name": "px-preflight",
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	// TODO append --dry-run to podSpec.Args, and -T dmthin
	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "px-preflight-ds-name",
			Namespace:       cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{Spec: podSpec},
		},
	}
	/* TODO  PWX-28826 Run DMThin pre-flight checks

	Operator starts a daemonset with oci-monitor pods in pre-flight mode (--preflight)
	Operator waits for all daemonset pods to be 1/1 (for this the oci-monitor in preflight mode must not exit).
		oci-monitor pod has to declare it’s done
	Operator deletes the preflight daemonset
	Operator reads Checks in the StorageNode for each node
	If Checks in any of the nodes
		fail: Operator sets backend to btrfs
			// Log, raise event and do nothing. Default already at btrfs
		all success: Operator sets backend to dmthin
			// Append dmthin backend use MiscArgs()
			toUpdate.Annotations[pxutil.AnnotationMiscArgs] = " -T dmthin"
	*/

	if u.cluster.Spec.ImagePullSecret != nil && *u.cluster.Spec.ImagePullSecret != "" {
		ds.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *u.cluster.Spec.ImagePullSecret,
			},
		)
	}

	if u.cluster.Spec.Placement != nil {
		if u.cluster.Spec.Placement.NodeAffinity != nil {
			ds.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: u.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(u.cluster.Spec.Placement.Tolerations) > 0 {
			ds.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range u.cluster.Spec.Placement.Tolerations {
				ds.Spec.Template.Spec.Tolerations = append(
					ds.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return u.k8sClient.Create(context.TODO(), ds)
}

func (u *preFlightPortworx) DeletePreFlight() error {
	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(u.k8sClient, component.PxPreFlightServiceAccountName, u.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(u.k8sClient, pxPreFlightClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(u.k8sClient, pxPreFlightClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDaemonSet(u.k8sClient, pxPreFlightDaemonSetName, u.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (u *preFlightPortworx) createServiceAccount(
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		u.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            component.PxPreFlightServiceAccountName,
				Namespace:       u.cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (u *preFlightPortworx) createClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		u.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: pxPreFlightClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{component.PxSCCName},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (u *preFlightPortworx) createClusterRoleBinding() error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		u.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: pxPreFlightClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      component.PxPreFlightServiceAccountName,
					Namespace: u.cluster.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pxPreFlightClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}
