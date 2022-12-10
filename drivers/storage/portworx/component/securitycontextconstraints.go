package component

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/go-version"
	ocp_secv1 "github.com/openshift/api/security/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	opcorev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
)

const (
	// SCCComponentName name a component for installing securityContextConstraints.
	SCCComponentName = "scc"
	// PxSCCName name of portworx securityContextConstraints
	PxSCCName = "portworx"
	// PxNodeWiperServiceAccountName name of portworx node wiper service account
	PxNodeWiperServiceAccountName = "px-node-wiper"
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
	if err := ocp_secv1.Install(s.k8sClient.Scheme()); err != nil {
		logrus.Errorf("Failed to add openshift objects to the scheme. %v", err)
	}
	if err := apiextensionsv1.AddToScheme(s.k8sClient.Scheme()); err != nil {
		logrus.Errorf("Failed to add api-extensions-v1 objects to the scheme. %v", err)
	}
}

func (s *scc) IsPausedForMigration(cluster *opcorev1.StorageCluster) bool {
	return false
}

func (s *scc) IsEnabled(cluster *opcorev1.StorageCluster) bool {
	return s.crdExists(cluster)
}

func (s *scc) crdExists(cluster *opcorev1.StorageCluster) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := s.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name: "securitycontextconstraints.security.openshift.io",
		},
		crd,
	)

	if err == nil {
		return true
	} else if errors.IsNotFound(err) {
		return false
	} else {
		logrus.WithError(err).Error("failed to get CRD SecurityContextConstraints")
		return false
	}
}

func (s *scc) Reconcile(cluster *opcorev1.StorageCluster) error {
	// Note SCC does not belong to namespace hence we don't set owner reference to StorageCluster.
	// By design cross-namespace owner reference is invalid.
	for _, scc := range s.getSCCs(cluster) {
		out := &ocp_secv1.SecurityContextConstraints{}
		err := s.k8sClient.Get(context.TODO(),
			types.NamespacedName{
				Name: scc.Name,
			},
			out)

		if errors.IsNotFound(err) {
			err = s.k8sClient.Create(context.TODO(), &scc)
			if err != nil {
				return fmt.Errorf("failed to create %s security context constraints: %s", scc.Name, err)
			}
		} else if err != nil {
			return err
		} else {
			// Skip reconcile the SCC if the annotation is set to true.
			enabled, err := strconv.ParseBool(out.Annotations[constants.AnnotationReconcileObject])
			if err == nil && !enabled {
				return nil
			}

			scc.ResourceVersion = out.ResourceVersion
			err = s.k8sClient.Update(context.TODO(), &scc)
			if err != nil {
				return fmt.Errorf("failed to update %s security context constraints: %s", scc.Name, err)
			}
		}
	}

	return nil
}

func (s *scc) Delete(cluster *opcorev1.StorageCluster) error {
	// Do not try to delete it if CRD does not exist, otherwise portworx would raise a cleanup failure warning.
	if !s.crdExists(cluster) {
		return nil
	}

	// Do not delete SCC during uninstallation, as it may be required by node wiper.
	// TODO: need to handle deletion in this case
	if cluster.DeletionTimestamp != nil &&
		cluster.Spec.DeleteStrategy != nil &&
		cluster.Spec.DeleteStrategy.Type != "" {
		return nil
	}

	for _, scc := range s.getSCCs(cluster) {
		err := s.k8sClient.Delete(context.TODO(), &scc)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

func (s *scc) MarkDeleted() {
}

func (s *scc) getSCCs(cluster *opcorev1.StorageCluster) []ocp_secv1.SecurityContextConstraints {
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
			Groups:  nil,
			Users: []string{
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, pxutil.PortworxServiceAccountName(cluster)),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, CSIServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, LhServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, PVCServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, CollectorServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, "px-node-wiper"),
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
