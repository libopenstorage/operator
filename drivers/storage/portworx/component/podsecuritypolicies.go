package component

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PSPComponentName name a component for installing podsecuritypolicies.
	PSPComponentName = "PodSecurityPolicies"
)

// RegisterPSPComponent registers a component for installing podsecuritypolicies.
func RegisterPSPComponent() {
	Register(PSPComponentName, &podsecuritypolicies{})
}

func init() {
	RegisterPSPComponent()
}

type podsecuritypolicies struct {
	k8sClient client.Client
}

func (p *podsecuritypolicies) Initialize(k8sClient client.Client, k8sVersion version.Version, scheme *runtime.Scheme, recorder record.EventRecorder) {
	p.k8sClient = k8sClient
}

func (p *podsecuritypolicies) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.PodSecurityPolicyEnabled(cluster)
}

func (p *podsecuritypolicies) Reconcile(cluster *corev1.StorageCluster) error {
	for _, policy := range portworxPodSecurityPolicies() {
		ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
		policy.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

		err := p.k8sClient.Create(context.TODO(), &policy)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create %s pod security policy: %s", policy.Name, err)
		}
	}
	return nil
}

func (p *podsecuritypolicies) Delete(cluster *corev1.StorageCluster) error {
	for _, policy := range portworxPodSecurityPolicies() {
		err := p.k8sClient.Delete(context.TODO(), &policy)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (p *podsecuritypolicies) MarkDeleted() {
}

func portworxPodSecurityPolicies() []policyv1beta1.PodSecurityPolicy {
	return []policyv1beta1.PodSecurityPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: constants.PrivilegedPSPName,
			},
			Spec: policyv1beta1.PodSecurityPolicySpec{
				Privileged:             true,
				HostNetwork:            true,
				ReadOnlyRootFilesystem: true,
				Volumes: []policyv1beta1.FSType{
					policyv1beta1.ConfigMap,
					policyv1beta1.Secret,
					policyv1beta1.HostPath,
					policyv1beta1.EmptyDir,
				},
				RunAsUser: policyv1beta1.RunAsUserStrategyOptions{
					Rule: policyv1beta1.RunAsUserStrategyRunAsAny,
				},
				FSGroup: policyv1beta1.FSGroupStrategyOptions{
					Rule: policyv1beta1.FSGroupStrategyRunAsAny,
				},
				SELinux: policyv1beta1.SELinuxStrategyOptions{
					Rule: policyv1beta1.SELinuxStrategyRunAsAny,
				},
				SupplementalGroups: policyv1beta1.SupplementalGroupsStrategyOptions{
					Rule: policyv1beta1.SupplementalGroupsStrategyRunAsAny,
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: constants.RestrictedPSPName,
			},
			Spec: policyv1beta1.PodSecurityPolicySpec{
				Privileged:             false,
				ReadOnlyRootFilesystem: true,
				Volumes: []policyv1beta1.FSType{
					policyv1beta1.ConfigMap,
					policyv1beta1.Secret,
					policyv1beta1.HostPath,
					policyv1beta1.EmptyDir,
				},
				RunAsUser: policyv1beta1.RunAsUserStrategyOptions{
					// TODO: update docker images to avoid using root user
					Rule: policyv1beta1.RunAsUserStrategyRunAsAny,
				},
				FSGroup: policyv1beta1.FSGroupStrategyOptions{
					Rule: policyv1beta1.FSGroupStrategyRunAsAny,
				},
				SELinux: policyv1beta1.SELinuxStrategyOptions{
					Rule: policyv1beta1.SELinuxStrategyRunAsAny,
				},
				SupplementalGroups: policyv1beta1.SupplementalGroupsStrategyOptions{
					Rule: policyv1beta1.SupplementalGroupsStrategyRunAsAny,
				},
			},
		},
	}
}
