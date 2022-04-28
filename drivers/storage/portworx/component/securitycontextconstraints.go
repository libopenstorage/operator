package component

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	ocp_secv1 "github.com/openshift/api/security/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	opcorev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

const (
	// SCCComponentName name a component for installing securityContextConstraints.
	SCCComponentName = "scc"
	// PxSCCName name of portworx securityContextConstraints
	PxSCCName = "portworx"
)

type scc struct {
	k8sClient client.Client
}

func (s *scc) Name() string {
	return SCCComponentName
}

func (s *scc) Priority() int32 {
	return int32(0)
}

func (s *scc) Initialize(k8sClient client.Client, k8sVersion version.Version, scheme *runtime.Scheme, recorder record.EventRecorder) {
	s.k8sClient = k8sClient
}

func (s *scc) IsPausedForMigration(cluster *opcorev1.StorageCluster) bool {
	return false
}

func (s *scc) IsEnabled(cluster *opcorev1.StorageCluster) bool {
	ocp_secv1.Install(s.k8sClient.Scheme())
	apiextensionsv1.AddToScheme(s.k8sClient.Scheme())
	list := &ocp_secv1.SecurityContextConstraintsList{}
	err := s.k8sClient.List(
		context.TODO(),
		list,
	)
	if err == nil {
		logrus.Debugf("security context constraints is enabled")
	} else {
		logrus.Debugf("security context constraints is disabled, %v", err)
	}

	return err == nil
}

func (s *scc) Reconcile(cluster *opcorev1.StorageCluster) error {
	for _, scc := range s.getSCCs(cluster.Namespace) {
		ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
		scc.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

		err := s.k8sClient.Create(context.TODO(), &scc)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create %s security context constraints: %s", scc.Name, err)
		}
	}
	return nil
}

func (s *scc) Delete(cluster *opcorev1.StorageCluster) error {
	if cluster.DeletionTimestamp != nil &&
		cluster.Spec.DeleteStrategy != nil &&
		cluster.Spec.DeleteStrategy.Type != "" {
		return nil
	}

	return nil
}

func (s *scc) MarkDeleted() {
}

func (s *scc) getSCCs(namespace string) []ocp_secv1.SecurityContextConstraints {
	return []ocp_secv1.SecurityContextConstraints{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxSCCName,
			},
			AllowHostDirVolumePlugin: true,
			AllowHostIPC:             true,
			AllowHostNetwork:         true,
			AllowHostPID:             true,
			AllowHostPorts:           true,
			AllowPrivilegeEscalation: boolPtr(true),
			AllowPrivilegedContainer: true,
			AllowedUnsafeSysctls:     []string{"*"},
			AllowedCapabilities: []corev1.Capability{
				ocp_secv1.AllowAllCapabilities,
			},
			FSGroup: ocp_secv1.FSGroupStrategyOptions{
				Type: ocp_secv1.FSGroupStrategyRunAsAny,
			},
			Priority:               pointer.Int32Ptr(2),
			ReadOnlyRootFilesystem: false,
			RunAsUser: ocp_secv1.RunAsUserStrategyOptions{
				Type: ocp_secv1.RunAsUserStrategyRunAsAny,
			},
			SELinuxContext: ocp_secv1.SELinuxContextStrategyOptions{
				Type: ocp_secv1.SELinuxStrategyRunAsAny,
			},
			SeccompProfiles: []string{"*"},
			SupplementalGroups: ocp_secv1.SupplementalGroupsStrategyOptions{
				Type: ocp_secv1.SupplementalGroupsStrategyRunAsAny,
			},
			Volumes: []ocp_secv1.FSType{"*"},
			Groups: []string{
				"system:cluster-admins",
				"system:nodes",
				"system:masters",
			},
			Users: []string{
				"system:admin",
				"system:serviceaccount:openshift-infra:build-controller",
				fmt.Sprintf("system:serviceaccount:%s:%s", namespace, PxSCCName),
			},
		},
	}
}

// RegisterSCCComponent registers a component for installing scc.
func RegisterSCCComponent() {
	Register(SCCComponentName, &scc{})
}

func init() {
	RegisterSCCComponent()
}
