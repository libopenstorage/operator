package component

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hashicorp/go-version"
	ocp_secv1 "github.com/openshift/api/security/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	opcorev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

const (
	// SCCComponentName name a component for installing securityContextConstraints.
	SCCComponentName = "scc"
	// PxSCCName name of portworx securityContextConstraints
	PxSCCName = "portworx"
	// PxRestrictedSCCName name of portworx-restricted securityContextConstraints
	PxRestrictedSCCName = "portworx-restricted"
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

func deepEqualScc(scc, existingScc *ocp_secv1.SecurityContextConstraints) bool {
	if !(scc.Priority == nil && existingScc.Priority == nil ||
		scc.Priority != nil && existingScc.Priority != nil && *scc.Priority == *existingScc.Priority) ||
		!(scc.AllowPrivilegeEscalation == nil && existingScc.AllowPrivilegeEscalation == nil ||
			scc.AllowPrivilegeEscalation != nil && existingScc.AllowPrivilegeEscalation != nil &&
				*scc.AllowPrivilegeEscalation == *existingScc.AllowPrivilegeEscalation) {
		return false
	}

	// Skip comparing auto-generated fields
	sccCopy := scc.DeepCopy()
	sccCopy.ResourceVersion = existingScc.ResourceVersion
	sccCopy.ObjectMeta.UID = existingScc.ObjectMeta.UID
	sccCopy.ObjectMeta.Generation = existingScc.ObjectMeta.Generation
	sccCopy.ObjectMeta.CreationTimestamp = existingScc.ObjectMeta.CreationTimestamp
	sccCopy.ObjectMeta.ManagedFields = existingScc.ObjectMeta.ManagedFields
	sccCopy.ObjectMeta.GenerateName = existingScc.ObjectMeta.GenerateName
	sccCopy.ObjectMeta.Finalizers = existingScc.ObjectMeta.Finalizers
	sccCopy.ObjectMeta.OwnerReferences = existingScc.ObjectMeta.OwnerReferences
	sccCopy.TypeMeta.Kind = existingScc.TypeMeta.Kind
	sccCopy.TypeMeta.APIVersion = existingScc.TypeMeta.APIVersion
	sccCopy.Priority = existingScc.Priority
	sccCopy.AllowPrivilegeEscalation = existingScc.AllowPrivilegeEscalation
	if scc.Groups == nil {
		sccCopy.Groups = make([]string, 0)
	}
	return reflect.DeepEqual(sccCopy, existingScc)
}

func (s *scc) Reconcile(cluster *opcorev1.StorageCluster) error {
	// Note SCC does not belong to namespace hence we don't set owner reference to StorageCluster.
	// By design cross-namespace owner reference is invalid.
	for _, scc := range s.getSCCs(cluster) {
		existingScc := ocp_secv1.SecurityContextConstraints{}
		err := s.k8sClient.Get(context.TODO(),
			types.NamespacedName{
				Name: scc.Name,
			},
			&existingScc)

		if errors.IsNotFound(err) {
			err = s.k8sClient.Create(context.TODO(), &scc)
			if err != nil {
				return fmt.Errorf("failed to create %s security context constraints: %s", scc.Name, err)
			}
		} else if err == nil {
			if !deepEqualScc(&scc, &existingScc) {
				scc.ResourceVersion = existingScc.ResourceVersion
				logrus.Infof("Updating %s security context constraints", scc.Name)
				err = s.k8sClient.Update(context.TODO(), &scc)
				if err != nil {
					return fmt.Errorf("failed to update %s security context constraints: %s", scc.Name, err)
				}
			}
		} else {
			return fmt.Errorf("failed to get %s security context constraints: %s", scc.Name, err)
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
	var sccPriority *int32

	if str, exists := cluster.Annotations[pxutil.AnnotationSCCPriority]; exists {
		n, err := strconv.ParseInt(str, 10, 32)
		if err == nil {
			t := int32(n)
			sccPriority = &t
		} else {
			logrus.WithError(err).Errorf("Invalid scc priority")
		}
	}

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
			Priority:               sccPriority,
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
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, CollectorServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, ServiceAccountNameTelemetry),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, "px-node-wiper"),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, "px-prometheus"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxRestrictedSCCName,
			},
			AllowHostDirVolumePlugin: true,
			AllowHostIPC:             false,
			AllowHostNetwork:         true,
			AllowHostPID:             false,
			AllowHostPorts:           false,
			AllowPrivilegeEscalation: boolPtr(true),
			AllowPrivilegedContainer: false,
			FSGroup: ocp_secv1.FSGroupStrategyOptions{
				Type: ocp_secv1.FSGroupStrategyMustRunAs,
			},
			ReadOnlyRootFilesystem: false,
			RequiredDropCapabilities: []corev1.Capability{
				"KILL",
				"MKNOD",
				"SETUID",
				"SETGID",
			},
			RunAsUser: ocp_secv1.RunAsUserStrategyOptions{
				Type: ocp_secv1.RunAsUserStrategyMustRunAsRange,
			},
			SELinuxContext: ocp_secv1.SELinuxContextStrategyOptions{
				Type: ocp_secv1.SELinuxStrategyMustRunAs,
			},
			SupplementalGroups: ocp_secv1.SupplementalGroupsStrategyOptions{
				Type: ocp_secv1.SupplementalGroupsStrategyRunAsAny,
			},
			Volumes: []ocp_secv1.FSType{
				ocp_secv1.FSTypeConfigMap,
				ocp_secv1.FSTypeDownwardAPI,
				ocp_secv1.FSTypeEmptyDir,
				ocp_secv1.FSTypeHostPath,
				ocp_secv1.FSTypePersistentVolumeClaim,
				ocp_secv1.FSProjected,
				ocp_secv1.FSTypeSecret,
			},
			Groups: nil,
			Users: []string{
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, CSIServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, LhServiceAccountName),
				fmt.Sprintf("system:serviceaccount:%s:%s", cluster.Namespace, PVCServiceAccountName),
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
