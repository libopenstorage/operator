package portworx

import (
	"io/ioutil"
	"sort"
	"testing"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestBasicRuncPodSpec(t *testing.T) {
	expected := getExpectedPodSpec(t, "testspec/runc.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll:        boolPtr(true),
					ForceUseDisks: boolPtr(true),
				},
				Env: []v1.EnvVar{
					{
						Name:  "TEST_KEY",
						Value: "TEST_VALUE",
					},
				},
				RuntimeOpts: map[string]string{
					"op1": "10",
					"op2": "20",
				},
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestPKSPodSpec(t *testing.T) {
	expected := getExpectedPodSpec(t, "testspec/pks.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationIsPKS: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func TestOpenshiftRuncPodSpec(t *testing.T) {
	expected := getExpectedPodSpec(t, "testspec/openshift_runc.yaml")

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1alpha1.PlacementSpec{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{
							{
								MatchExpressions: []v1.NodeSelectorRequirement{
									{
										Key:      "px/enabled",
										Operator: v1.NodeSelectorOpNotIn,
										Values:   []string{"false"},
									},
									{
										Key:      "node-role.kubernetes.io/infra",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
									{
										Key:      "node-role.kubernetes.io/master",
										Operator: v1.NodeSelectorOpDoesNotExist,
									},
								},
							},
						},
					},
				},
			},
			Kvdb: &corev1alpha1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1alpha1.CommonConfig{
				Storage: &corev1alpha1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual := driver.GetStoragePodSpec(cluster)

	assertPodSpecEqual(t, expected, &actual)
}

func getExpectedPodSpec(t *testing.T, fileName string) *v1.PodSpec {
	json, err := ioutil.ReadFile(fileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	ds, ok := obj.(*v1beta1.DaemonSet)
	assert.True(t, ok, "Expected daemon set object")

	return &ds.Spec.Template.Spec
}

func assertPodSpecEqual(t *testing.T, expected, actual *v1.PodSpec) {
	assert.Equal(t, expected.Affinity, actual.Affinity)
	assert.Equal(t, expected.HostNetwork, actual.HostNetwork)
	assert.Equal(t, expected.RestartPolicy, actual.RestartPolicy)
	assert.Equal(t, expected.ServiceAccountName, actual.ServiceAccountName)
	assert.Equal(t, expected.ImagePullSecrets, actual.ImagePullSecrets)
	assert.Equal(t, expected.Tolerations, actual.Tolerations)

	sort.Sort(volumesByName(expected.Volumes))
	sort.Sort(volumesByName(actual.Volumes))
	assert.Equal(t, expected.Volumes, actual.Volumes)

	assert.Equal(t, len(expected.Containers), len(actual.Containers))
	for i, e := range expected.Containers {
		assertContainerEqual(t, e, actual.Containers[i])
	}
}

func assertContainerEqual(t *testing.T, expected, actual v1.Container) {
	assert.Equal(t, expected.Name, actual.Name)
	assert.Equal(t, expected.Image, actual.Image)
	assert.Equal(t, expected.ImagePullPolicy, actual.ImagePullPolicy)
	assert.Equal(t, expected.TerminationMessagePath, actual.TerminationMessagePath)
	assert.Equal(t, expected.LivenessProbe, actual.LivenessProbe)
	assert.Equal(t, expected.ReadinessProbe, actual.ReadinessProbe)
	assert.Equal(t, expected.SecurityContext, actual.SecurityContext)

	sort.Strings(expected.Args)
	sort.Strings(actual.Args)
	assert.Equal(t, expected.Args, actual.Args)

	sort.Sort(envByName(expected.Env))
	sort.Sort(envByName(actual.Env))
	assert.Equal(t, expected.Env, actual.Env)

	sort.Sort(volumeMountsByName(expected.VolumeMounts))
	sort.Sort(volumeMountsByName(actual.VolumeMounts))
	assert.Equal(t, expected.VolumeMounts, actual.VolumeMounts)
}

type volumesByName []v1.Volume

func (v volumesByName) Len() int      { return len(v) }
func (v volumesByName) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func (v volumesByName) Less(i, j int) bool {
	return v[i].Name < v[j].Name
}

type volumeMountsByName []v1.VolumeMount

func (v volumeMountsByName) Len() int      { return len(v) }
func (v volumeMountsByName) Swap(i, j int) { v[i], v[j] = v[j], v[i] }
func (v volumeMountsByName) Less(i, j int) bool {
	return v[i].Name < v[j].Name
}

type envByName []v1.EnvVar

func (h envByName) Len() int      { return len(h) }
func (h envByName) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h envByName) Less(i, j int) bool {
	return h[i].Name < h[j].Name
}
