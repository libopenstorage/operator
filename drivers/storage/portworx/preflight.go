package portworx

import (
	"context"
	"fmt"
	"regexp"
	"strings"

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
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	pxPreFlightClusterRoleName        = "px-pre-flight"
	pxPreFlightClusterRoleBindingName = "px-pre-flight"
	pxPreFlightDaemonSetName          = "px-pre-flight"
	// PxPreFlightServiceAccountName name of portworx pre flight service account
	PxPreFlightServiceAccountName = "px-pre-flight"
	// DefCmetaData default metadata cloud device for DMthin
	DefCmetaData = "type=gp3,size=64"
)

// PreFlightPortworx provides a set of APIs to uninstall portworx
type PreFlightPortworx interface {
	// RunPreFlight runs the pre-flight  daemonset
	RunPreFlight() error
	// GetPreFlightStatus returns the status of the pre-flight daemonset
	// returns the no. of completed, in progress and total pods
	GetPreFlightStatus() (int32, int32, int32, error)
	// GetPreFlightPods returns the pods of the pre-flight daemonset
	GetPreFlightPods() ([]*v1.Pod, error)
	// ProcessPreFlightResults process StorageNode status checks
	ProcessPreFlightResults(recorder record.EventRecorder, storageNodes []*corev1.StorageNode) error
	// DeletePreFlight deletes the pre-flight daemonset
	DeletePreFlight() error
}

type preFlightPortworx struct {
	cluster   *corev1.StorageCluster
	k8sClient client.Client
	podSpec   v1.PodSpec
	hardFail  bool
}

// Existing dmThin strings
var dmthinRegex = regexp.MustCompile("(?i)(dmthin|PX-StoreV2|px-store-v2)")

// NewPreFlighter returns an implementation of PreFlightPortworx interface
func NewPreFlighter(
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
	podSpec v1.PodSpec) PreFlightPortworx {
	return &preFlightPortworx{
		cluster:   cluster,
		k8sClient: k8sClient,
		podSpec:   podSpec,
	}
}

func getPreFlightPodsFromNamespace(k8sClient client.Client, namespace string) (*appsv1.DaemonSet, []*v1.Pod, error) {
	ds := &appsv1.DaemonSet{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pxPreFlightDaemonSetName,
			Namespace: namespace,
		},
		ds,
	)
	if err != nil {
		return ds, nil, err
	}

	pods, err := k8sutil.GetDaemonSetPods(k8sClient, ds)

	return ds, pods, err
}

// GetPreFlightPods returns the pods of the pre-flight daemonset
func (u *preFlightPortworx) GetPreFlightPods() ([]*v1.Pod, error) {
	_, pods, err := getPreFlightPodsFromNamespace(u.k8sClient, u.cluster.Namespace)
	return pods, err
}

func (u *preFlightPortworx) GetPreFlightStatus() (int32, int32, int32, error) {
	ds, pods, err := getPreFlightPodsFromNamespace(u.k8sClient, u.cluster.Namespace)
	if err != nil {
		return -1, -1, -1, err
	}
	totalPods := ds.Status.DesiredNumberScheduled
	completedPods := 0
	for _, pod := range pods {
		if len(pod.Status.ContainerStatuses) > 0 {
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Name == "portworx" && containerStatus.Ready {
					completedPods++
				}
			}
		}
	}
	logrus.Infof("Pre-flight Status: Completed [%v] InProgress [%v] Total Pods [%v]", completedPods, totalPods-int32(completedPods), totalPods)
	return int32(completedPods), totalPods - int32(completedPods), totalPods, nil
}

func (u *preFlightPortworx) RunPreFlight() error {
	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())

	err := u.createServiceAccount(ownerRef)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			logrus.Infof("runPreFlight: ServiceAccount already exists, skipping...")
		} else {
			return err
		}
	}

	err = u.createClusterRole()
	if err != nil {
		if errors.IsAlreadyExists(err) {
			logrus.Infof("runPreFlight: ClusterRole already exists, skipping...")
		} else {
			return err
		}
	}

	err = u.createClusterRoleBinding()
	if err != nil {
		if errors.IsAlreadyExists(err) {
			logrus.Infof("runPreFlight: ClusterRoleBinding already exists, skipping...")
		} else {
			return err
		}
	}

	// Create daemonset from podSpec
	labels := map[string]string{
		"name": pxPreFlightDaemonSetName,
	}

	preflightDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxPreFlightDaemonSetName,
			Namespace:       u.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: u.podSpec,
			},
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

	   	u.cluster.Annotations[pxutil.AnnotationMiscArgs] = strings.TrimSpace(miscArgs)
	*/

	checkArgs := func(args []string) {
		for i, arg := range args {
			if arg == "-T" {
				if dmthinRegex.Match([]byte(args[i+1])) {
					u.hardFail = true
				}
			}
		}
	}

	// Check for pre-existing DMthin in container args
	checkArgs(u.podSpec.Containers[0].Args)

	if !u.hardFail {
		// If Dmthin param does not exist add it
		preflightDS.Spec.Template.Spec.Containers[0].Args = append([]string{"-T", "dmthin"},
			preflightDS.Spec.Template.Spec.Containers[0].Args...)
	} else {
		logrus.Infof("runPreflight: running pre-flight with existing PX-StoreV2 param, hard fail check enabled")
	}

	// Add pre-flight param
	preflightDS.Spec.Template.Spec.Containers[0].Args = append([]string{"--pre-flight"},
		preflightDS.Spec.Template.Spec.Containers[0].Args...)

	if u.cluster.Spec.ImagePullSecret != nil && *u.cluster.Spec.ImagePullSecret != "" {
		preflightDS.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *u.cluster.Spec.ImagePullSecret,
			},
		)
	}

	preflightDS.Spec.Template.Spec.ServiceAccountName = PxPreFlightServiceAccountName

	if u.cluster.Spec.Placement != nil {
		if u.cluster.Spec.Placement.NodeAffinity != nil {
			preflightDS.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: u.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(u.cluster.Spec.Placement.Tolerations) > 0 {
			preflightDS.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range u.cluster.Spec.Placement.Tolerations {
				preflightDS.Spec.Template.Spec.Tolerations = append(
					preflightDS.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	err = u.k8sClient.Create(context.TODO(), preflightDS)
	if err != nil {
		logrus.Errorf("RunPreFlight: error creating: %v", err)
	}

	return err
}

func (u *preFlightPortworx) ProcessPreFlightResults(recorder record.EventRecorder, storageNodes []*corev1.StorageNode) error {
	logrus.Infof("pre-flight: process pre-flight results...")

	passed := true
	for _, node := range storageNodes {
		logrus.Infof("storageNode[%s]: %#v ", node.Name, node.Status.Checks)
		if len(node.Status.Checks) > 1 {
			for _, check := range node.Status.Checks {
				if check.Type != "status" {
					msg := fmt.Sprintf("%s pre-flight check ", check.Type)
					if check.Success {
						msg = msg + "passed: " + check.Reason
						k8sutil.InfoEvent(recorder, u.cluster, util.PassPreFlight, msg)
					} else {
						msg = msg + "failed: " + check.Reason
						k8sutil.WarningEvent(recorder, u.cluster, util.FailedPreFlight, msg)
					}
				}
			}
			passed = false
		}
	}

	if passed {
		if !u.hardFail { // Enable DMthin via misc args if not enabled already
			u.cluster.Annotations[pxutil.AnnotationMiscArgs] = strings.TrimSpace(
				u.cluster.Annotations[pxutil.AnnotationMiscArgs] + " -T dmthin")
			k8sutil.InfoEvent(recorder, u.cluster, util.PassPreFlight, "Enabling PX-StoreV2")
		} else {
			k8sutil.InfoEvent(recorder, u.cluster, util.PassPreFlight, "PX-StoreV2 currently enabled")
		}

		// Add 64G metadata drive.
		if u.cluster.Spec.CloudStorage == nil {
			u.cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
		}

		if u.cluster.Spec.CloudStorage.SystemMdDeviceSpec == nil {
			cmetaData := DefCmetaData
			u.cluster.Spec.CloudStorage.SystemMdDeviceSpec = &cmetaData
		}
	} else {
		if !u.hardFail { // Enable DMthin via misc args if not enabled already
			k8sutil.InfoEvent(recorder, u.cluster, util.PassPreFlight, "Not enabling PX-StoreV2")
		} else { // hardFail is enabled, fail if any pre-flight check fails.
			err := fmt.Errorf("PX-StoreV2 pre-check failed")
			k8sutil.WarningEvent(recorder, u.cluster, util.FailedPreFlight, err.Error())
			return err
		}
	}

	return nil
}

func (u *preFlightPortworx) DeletePreFlight() error {
	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(u.k8sClient, PxPreFlightServiceAccountName, u.cluster.Namespace, *ownerRef); err != nil {
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
				Name:            PxPreFlightServiceAccountName,
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

			//  Portworx roles taken from drivers/storage/portworx/component/portworx_basic.go
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list", "watch"},
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
					Resources: []string{"pods/exec"},
					Verbs:     []string{"create"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"persistentvolumeclaims", "persistentvolumes"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "list", "create", "update"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"endpoints"},
					Verbs:     []string{"get", "list", "create", "update", "delete"},
				},
				{
					APIGroups: []string{"portworx.io"},
					Resources: []string{"volumeplacementstrategies"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"storageclasses", "csinodes"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"storage.k8s.io"},
					Resources: []string{"volumeattachments"},
					Verbs:     []string{"get", "list", "create", "delete", "update"},
				},
				{
					APIGroups: []string{"stork.libopenstorage.org"},
					Resources: []string{"backuplocations"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"", "events.k8s.io"},
					Resources: []string{"events"},
					Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
				},
				{
					APIGroups: []string{"core.libopenstorage.org"},
					Resources: []string{"*"},
					Verbs:     []string{"*"},
				},
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
				{
					APIGroups: []string{"certificates.k8s.io"},
					Resources: []string{"certificatesigningrequests"},
					Verbs:     []string{"get", "list", "create", "watch", "delete", "update"},
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
					Name:      PxPreFlightServiceAccountName,
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
