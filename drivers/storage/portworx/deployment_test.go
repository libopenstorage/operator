package portworx

import (
	"encoding/json"
	"os"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	"github.com/libopenstorage/cloudops"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/cloudprovider"
	"github.com/libopenstorage/operator/pkg/preflight"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	appsv1 "k8s.io/api/apps/v1"
)

func TestBasicRuncPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/runc.yaml")
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
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
										Key:      "kubernetes.io/os",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"linux"},
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
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
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
				},
			},
		},
	}

	driver := portworx{}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithCustomKubeletDir(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	// expected := getExpectedPodSpecFromDaemonset(t, "testspec/runc.yaml")
	nodeName := "testNode"

	customKubeletPath := "/data/kubelet"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
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
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll:        boolPtr(true),
					ForceUseDisks: boolPtr(true),
				},
				Env: []v1.EnvVar{
					{
						Name:  "TEST_KEY",
						Value: "TEST_VALUE",
					},
					{
						Name:  pxutil.EnvKeyKubeletDir,
						Value: customKubeletPath,
					},
				},
				RuntimeOpts: map[string]string{
					"op1": "10",
				},
			},
		},
	}

	driver := portworx{}

	// Case 1: When portworx version is lesser than 2.13
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	// CSI driver path
	var ok bool
	for _, v := range actual.Volumes {
		if v.Name == "csi-driver-path" && v.VolumeSource.HostPath.Path == customKubeletPath+"/csi-plugins/com.openstorage.pxd" {
			ok = true
		}
	}
	require.True(t, ok)

	// Case 2: When portworx version is greater than or equal to 2.13, csi-driver-registrar becomes a part of portworx-api daemonset
	// Hence csi-driver-path is also defined in the daemonset instead of the portworx storage pod
	cluster.Spec.Image = "portworx/oci-monitor:2.14.3"

	reregisterComponents()
	k8sClient := testutil.FakeK8sClient()

	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(10))
	require.NoError(t, err)

	err = driver.PreInstall(cluster)
	require.NoError(t, err)

	// CSI driver path
	ds := &appsv1.DaemonSet{}
	err = testutil.Get(k8sClient, ds, component.PxAPIDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	logrus.Infof("Volumes %+v", ds.Spec.Template.Spec.Volumes)

	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.Name == "csi-driver-path" && v.HostPath.Path == customKubeletPath+"/csi-plugins/com.openstorage.pxd" {
			ok = true
		}
	}
	require.True(t, ok)

}

func TestPodSpecWithImagePullSecrets(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/oci-monitor:2.0.3.4",
			ImagePullSecret: stringPtr("px-secret"),
		},
	}

	expectedPullSecret := v1.LocalObjectReference{
		Name: "px-secret",
	}
	expectedRegistryEnv := v1.EnvVar{
		Name: "REGISTRY_CONFIG",
		ValueFrom: &v1.EnvVarSource{
			SecretKeyRef: &v1.SecretKeySelector{
				Key: ".dockerconfigjson",
				LocalObjectReference: v1.LocalObjectReference{
					Name: "px-secret",
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 6)
	var regConfigEnv *v1.EnvVar
	var regSecretEnv *v1.EnvVar
	for _, env := range actual.Containers[0].Env {
		if env.Name == "REGISTRY_CONFIG" {
			regConfigEnv = env.DeepCopy()
		} else if env.Name == "REGISTRY_SECRET" {
			regSecretEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, expectedRegistryEnv, *regConfigEnv)
	assert.Nil(t, regSecretEnv)

	// TestCase: Portworx version is newer than 2.3.2
	cluster.Spec.Image = "portworx/oci-monitor:2.3.2"

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
	assert.Len(t, actual.Containers[0].Env, 6)
	regConfigEnv = nil
	for _, env := range actual.Containers[0].Env {
		if env.Name == "REGISTRY_CONFIG" {
			regConfigEnv = env.DeepCopy()
		} else if env.Name == "REGISTRY_SECRET" {
			regSecretEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, "px-secret", regSecretEnv.Value)
	assert.Nil(t, regConfigEnv)

	// kvdb pod spec
	actual, err = driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Len(t, actual.ImagePullSecrets, 1)
	assert.Equal(t, expectedPullSecret, actual.ImagePullSecrets[0])
}

func TestPodSpecWithTolerations(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	tolerations := []v1.Toleration{
		{
			Key:      "must-exist",
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoExecute,
		},
		{
			Key:      "foo",
			Operator: v1.TolerationOpEqual,
			Value:    "bar",
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
		},
	}

	driver := portworx{}
	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.ElementsMatch(t, tolerations, podSpec.Tolerations)

	kvdbPodSpec, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.NotNil(t, kvdbPodSpec)
	require.ElementsMatch(t, tolerations, kvdbPodSpec.Tolerations)
}

func TestPodSpecWithPortworxContainerResources(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{},
	}

	// Case 1: Empty resources during deployment
	driver := portworx{}
	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, v1.ResourceRequirements{}, podSpec.Containers[0].Resources)

	// Case 2: Add resources during deployment
	resources := v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("4"),
		},
	}
	cluster.Spec.Resources = &resources

	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, resources, podSpec.Containers[0].Resources)

	// Memory limit and CPU limit
	cluster.Spec.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("400m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceCPU:    resource.MustParse("800m"),
		},
	}

	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, *cluster.Spec.Resources, podSpec.Containers[0].Resources)
	require.True(t, strings.Contains(strings.Join(podSpec.Containers[0].Args, " "), "--cpus 0.8"))
	require.True(t, strings.Contains(strings.Join(podSpec.Containers[0].Args, " "), "--memory 8589934592"))

	cluster.Spec.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("4"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceCPU:    resource.MustParse("8"),
		},
	}

	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Len(t, podSpec.Containers, 1)
	assert.Equal(t, *cluster.Spec.Resources, podSpec.Containers[0].Resources)
	require.True(t, strings.Contains(strings.Join(podSpec.Containers[0].Args, " "), "--cpus 8"))
	require.True(t, strings.Contains(strings.Join(podSpec.Containers[0].Args, " "), "--memory 8589934592"))
}

func TestPodSpecWithEnvOverrides(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxSecretsNamespace,
						Value: "custom",
					},
					{
						Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
						Value: "300",
					},
				},
			},
		},
	}

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/portworxPodEnvOverride.yaml")

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithPriorityClassName(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"
	priorityClassName := "high-priority"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image:             "portworx/oci-monitor:2.1.1",
			PriorityClassName: priorityClassName,
		},
	}
	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Equal(t, priorityClassName, actual.PriorityClassName)
}

func TestGetKVDBPodSpec(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{},
	}

	expected := getExpectedPodSpec(t, "testspec/kvdbPodDefault.yaml")
	actual, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assertPodSpecEqual(t, expected, &actual)

	// custom port
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	expected = getExpectedPodSpec(t, "testspec/kvdbPodCustomPort.yaml")
	actual, err = driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assertPodSpecEqual(t, expected, &actual)

	// retry w/ a custom image
	overridden := "foobar.acme.org/some-repo/pauzaner:1.2.3"
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Pause: overridden,
	}
	expected.Containers[0].Image = overridden
	actual, err = driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecWithCustomServiceAccount(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.8",
	}

	nodeName := "testNode"
	driver := portworx{}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  pxutil.EnvKeyPortworxServiceAccount,
						Value: "custom-px-sa",
					},
				},
			},
		},
	}

	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)
	require.Equal(t, "custom-px-sa", podSpec.ServiceAccountName)

	kvdbPodSpec, err := driver.GetKVDBPodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.Equal(t, "custom-px-sa", kvdbPodSpec.ServiceAccountName)
}

func TestAutoNodeRecoveryTimeoutEnvForPxVersion2_6(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.5.1",
		},
	}
	recoveryEnv := v1.EnvVar{
		Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
		Value: "1500",
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.Contains(t, actual.Containers[0].Env, recoveryEnv)

	cluster.Spec.Image = "portworx/oci-monitor:2.6.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	require.NotContains(t, actual.Containers[0].Env, recoveryEnv)
}

func TestVarLibOsdMountForPxVersion2_9_1(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.9.1",
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/runc_2.9.1.yaml")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected, &actual)

	// PKS environment
	cluster.Annotations = map[string]string{
		pxutil.AnnotationIsPKS: "true",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/pks_2.9.1.yaml")
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected, &actual)
}

func TestExtraVolumeUnique(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	volumeSpec := corev1.VolumeSpec{
		Name: "volume1",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: "/path123",
			},
		},
		MountPath: "/mnt/uniq123",
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Volumes: []corev1.VolumeSpec{
				volumeSpec,
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	// custom `-v /path123:/mnt/uniq123` should be the last volume in the POD-spec
	customVolumeName := pxutil.UserVolumeName(volumeSpec.Name)
	lastActualVol := actual.Volumes[len(actual.Volumes)-1]
	assert.Equal(t, customVolumeName, lastActualVol.Name)
	assert.Equal(t, "/path123", lastActualVol.HostPath.Path)

	lastContainerVol := actual.Containers[0].VolumeMounts[len(actual.Containers[0].VolumeMounts)-1]
	assert.Equal(t, customVolumeName, lastContainerVol.Name)
	assert.Equal(t, volumeSpec.MountPath, lastContainerVol.MountPath)

	// make sure it's unique
	cnt := 0
	for _, v := range actual.Volumes {
		if v.Name == customVolumeName {
			cnt++
		}
	}
	assert.Equal(t, 1, cnt)

	cnt = 0
	for _, v := range actual.Containers[0].VolumeMounts {
		if v.Name == customVolumeName {
			cnt++
		}
	}
	assert.Equal(t, 1, cnt)
}

func TestExtraVolumeOverrideExisting(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"
	hostDir, containerDir := "/mnt/custom/test-cores", "/var/cores"

	volumeSpec := corev1.VolumeSpec{
		Name: "volume1",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: hostDir,
			},
		},
		MountPath: containerDir,
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Volumes: []corev1.VolumeSpec{
				volumeSpec,
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	customVolumeName := pxutil.UserVolumeName(volumeSpec.Name)

	found := false
	for _, v := range actual.Volumes {
		if v.Name == customVolumeName {
			assert.Equal(t, hostDir, v.HostPath.Path)
			found = true
		}
	}
	assert.True(t, found)

	found = false
	for _, v := range actual.Containers[0].VolumeMounts {
		if v.Name == customVolumeName {
			assert.Equal(t, containerDir, v.MountPath)
			found = true
		} else {
			assert.NotEqual(t, containerDir, v.MountPath)
		}
	}
	assert.True(t, found)
}

func TestStcMountsAndOverrides(t *testing.T) {
	const propNil = v1.MountPropagationMode("")

	mk := func(name, src, dest string, isRO bool, prop v1.MountPropagationMode) volumeInfo {
		pprop := &prop
		if prop == propNil {
			pprop = nil
		}
		return volumeInfo{
			name:             name,
			hostPath:         src,
			mountPath:        dest,
			readOnly:         isRO,
			mountPropagation: pprop,
		}
	}

	_2vs := func(vi []volumeInfo) []corev1.VolumeSpec {
		ret := make([]corev1.VolumeSpec, len(vi))
		for i, v := range vi {
			ret[i] = corev1.VolumeSpec{
				Name: v.name,
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: v.hostPath,
					},
				},
				ReadOnly:         v.readOnly,
				MountPath:        v.mountPath,
				MountPropagation: v.mountPropagation,
			}
		}
		return ret
	}

	_2vm := func(vi []volumeInfo) []v1.VolumeMount {
		ret := make([]v1.VolumeMount, len(vi))
		for i, v := range vi {
			ret[i] = v1.VolumeMount{
				Name:             v.name,
				MountPath:        v.mountPath,
				MountPropagation: v.mountPropagation,
				ReadOnly:         v.readOnly,
			}
		}
		return ret
	}

	_copy := func(vi []volumeInfo, add ...volumeInfo) []volumeInfo {
		ret := make([]volumeInfo, 0, len(vi)+len(add))
		ret = append(ret, vi...)
		return append(ret, add...)
	}

	_without := func(vi []volumeInfo, remove ...string) []volumeInfo {
		ret := make([]volumeInfo, 0, len(vi))
		for _, v := range vi {
			found := false
			for _, toRm := range remove {
				if v.name == toRm {
					found = true
					break
				}
			}
			if !found {
				ret = append(ret, v)
			}
		}
		return ret
	}

	expectCommonVolumes := []volumeInfo{
		mk("pxlogs", "", "", false, propNil),
		mk("varlibosd", "/var/lib/osd", "/var/lib/osd", false, v1.MountPropagationBidirectional),
		mk("diagsdump", "/var/cores", "/var/cores", false, propNil),
		mk("etcpwx", "/etc/pwx", "/etc/pwx", false, propNil),
		mk("journalmount1", "/var/run/log", "/var/run/log", true, propNil),
		mk("journalmount2", "/var/log", "/var/log", true, propNil),
	}

	testVols := getCommonVolumeList(pxVer2_13_8)
	for i := range testVols {
		testVols[i].pks = nil
	}
	assert.Equal(t, expectCommonVolumes, testVols)
	// note, removing "PKS-specific" `pxlogs`
	expectCommonVolumes = expectCommonVolumes[1:]

	testData := []struct {
		stcVols    []volumeInfo
		expectVols []volumeInfo
	}{
		// baseline - no extra vols
		{nil, expectCommonVolumes},
		// add 1 custom volume
		{
			[]volumeInfo{mk("test0", "/mnt/test0", "/mnt/test0", false, propNil)},
			_copy(
				_without(expectCommonVolumes, ""),
				mk("user-test0", "/mnt/test0", "/mnt/test0", false, propNil),
			),
		},
		// override existing
		{
			[]volumeInfo{mk("diagsdump", "/mnt/test0", "/var/cores", false, propNil)},
			_copy(
				_without(expectCommonVolumes, "diagsdump"),
				mk("user-diagsdump", "/mnt/test0", "/var/cores", false, propNil),
			),
		},
		// new + override existing
		{
			[]volumeInfo{
				mk("test1", "/mnt/test1", "/mnt/test1", false, v1.MountPropagationBidirectional),
				mk("diagsdump", "/mnt/test0", "/var/cores", false, propNil),
			},
			_copy(
				_without(expectCommonVolumes, "diagsdump"),
				mk("user-test1", "/mnt/test1", "/mnt/test1", false, v1.MountPropagationBidirectional),
				mk("user-diagsdump", "/mnt/test0", "/var/cores", false, propNil),
			),
		},
	}

	for i, td := range testData {
		tr := &template{
			cluster: &corev1.StorageCluster{
				Spec: corev1.StorageClusterSpec{
					Volumes: _2vs(td.stcVols),
				},
			},
		}

		expected := _2vm(td.expectVols)
		got := tr.mountsFromVolInfo(expectCommonVolumes)

		// volumes order is not important.. so let's re-sort
		sort.SliceStable(expected, func(i, j int) bool { return expected[i].Name > expected[j].Name })
		sort.SliceStable(got, func(i, j int) bool { return got[i].Name > got[j].Name })

		assert.Equal(t, expected, got,
			"Expectation failed for test #%d / %v", i+1, td.stcVols)
	}
}

func TestPodSpecWithTLS(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"
	caCertFileName := stringPtr("/etc/pwx/testCA.crt")
	serverCertFileName := stringPtr("/etc/pwx/testServer.crt")
	serverKeyFileName := stringPtr("/etc/pwx/testServer.key")

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	// Test1: user specifies all cert files
	logrus.Tracef("---Test1---")
	cluster := testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, serverKeyFileName)
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec after defaults = \n, %v", string(s))
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	// validate
	validatePodSpecWithTLS("with all files specified", t,
		caCertFileName,
		serverCertFileName,
		serverKeyFileName, actual)

	// Test2: user specifies ca cert as a file, cert/key in the same secret,
	cluster = testutil.CreateClusterWithTLS(caCertFileName, nil, nil)
	cluster.Spec.Security.TLS.ServerCert = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "testserversecret",
			SecretKey:  "testserversecret.crt",
		},
	}
	cluster.Spec.Security.TLS.ServerKey = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "testserversecret",
			SecretKey:  "testserversecret.key",
		},
	}
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	// validate
	s, _ = json.MarshalIndent(actual, "", "\t")
	t.Logf("pod spec under validation = \n, %v", string(s))
	validatePodSpecWithTLS("with server/key in same secrets, ca file specified", t,
		caCertFileName,
		stringPtr(path.Join(pxutil.DefaultTLSServerCertMountPath, "testserversecret.crt")),
		stringPtr(path.Join(pxutil.DefaultTLSServerKeyMountPath, "testserversecret.key")), actual)
	// validate that volume and volume mount exist
	validateVolumeAndMounts(t, actual, "testserversecret", "testserversecret.crt", pxutil.DefaultTLSServerCertMountPath)
	validateVolumeAndMounts(t, actual, "testserversecret", "testserversecret.key", pxutil.DefaultTLSServerKeyMountPath)
}

func validateVolumeAndMounts(t *testing.T, actual v1.PodSpec, secretName, secretKey, mountFolder string) {
	var volumeMount *v1.VolumeMount = nil
	for _, v := range actual.Containers[0].VolumeMounts {
		if v.MountPath == mountFolder {
			volumeMount = v.DeepCopy()
			break
		}
	}
	assert.NotNil(t, volumeMount)

	var volume *v1.Volume = nil
	for _, v := range actual.Volumes {
		if v.Name == volumeMount.Name {
			volume = v.DeepCopy()
			break
		}
	}
	assert.NotNil(t, volume) // validate secret is mounted
	assert.NotNil(t, volume.Secret)
	assert.NotEmpty(t, volume.Secret.Items) // volume has reference to required key
	assert.Equal(t, 1, len(volume.Secret.Items))
	assert.Equal(t, secretKey, volume.Secret.Items[0].Key)
	assert.Equal(t, secretKey, volume.Secret.Items[0].Path)
}

// validateTLSEnvAndParams is a helper method used by TestPodSpecWithTLS
func validatePodSpecWithTLS(testName string, t *testing.T, apirootcaArg, apicertArg, apikeyArg *string, actual v1.PodSpec) {
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-a",
		"-secret_type", "k8s",
		"-apidisclientauth",
	}
	if apirootcaArg != nil {
		expectedArgs = append(expectedArgs, "-apirootca", *apirootcaArg)
	}
	if apicertArg != nil {
		expectedArgs = append(expectedArgs, "-apicert", *apicertArg)
	}
	if apikeyArg != nil {
		expectedArgs = append(expectedArgs, "-apikey", *apikeyArg)
	}

	// validate that PX_ENABLE_TLS is set
	expectedVal := "true"
	actualVal := ""
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxEnableTLS {
			actualVal = env.Value
			break
		}
	}
	assert.Equal(t, expectedVal, actualVal, testName)

	// validate that PX_ENFORCE_TLS is set
	expectedVal = "true"
	actualVal = ""
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxEnforceTLS {
			actualVal = env.Value
			break
		}
	}
	assert.Equal(t, expectedVal, actualVal, testName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args, testName)
}

func TestPodSpecWithKvdbSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Kvdb: &corev1.KvdbSpec{
				Endpoints: []string{
					"endpoint-1",
					"endpoint-2",
					"endpoint-3",
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k", "endpoint-1,endpoint-2,endpoint-3",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should have both bootstrap and endpoints
	cluster.Spec.Kvdb.Internal = true
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-k", "endpoint-1,endpoint-2,endpoint-3",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints is nil
	cluster.Spec.Kvdb.Endpoints = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Should take bootstrap only if endpoints are empty
	cluster.Spec.Kvdb.Endpoints = []string{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecForVsphere(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"
	k8sClient := testutil.FakeK8sClient()

	originalSpec := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image:        "portworx/oci-monitor:3.0.0",
			CloudStorage: &corev1.CloudStorageSpec{},
		},
	}
	// TestCase 1: Detect VSPHERE_VCENTER and set provider
	cluster := originalSpec.DeepCopy()

	env := make([]v1.EnvVar, 1)
	env[0].Name = "VSPHERE_VCENTER"
	env[0].Value = "some.vcenter.server.com"
	cluster.Spec.Env = env

	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	expectedArgs := []string{
		"-cloud_provider", "vsphere",
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-secret_type", "k8s",
	}

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	_, ok := cluster.Annotations[pxutil.AnnotationPreflightCheck]
	require.True(t, ok)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase 2: VSPHERE_VCENTER  not found, don't expect -cloud_provider
	cluster = originalSpec.DeepCopy()
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-secret_type", "k8s",
	}

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase 3: provider set explicitly
	cluster = originalSpec.DeepCopy()
	provider := string(cloudops.AWS)
	cluster.Spec.CloudStorage.Provider = &provider
	driver = portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	expectedArgs = []string{
		"-cloud_provider", string(cloudops.AWS),
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-secret_type", "k8s",
	}

	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestPodSpecWithNetworkSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Network: &corev1.NetworkSpec{
					DataInterface: stringPtr("data-intf"),
					MgmtInterface: stringPtr("mgmt-intf"),
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-d", "data-intf",
		"-m", "mgmt-intf",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only data interface is given
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-d", "data-intf",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Mgmt interface given but empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr("data-intf"),
		MgmtInterface: stringPtr(""),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Only management interface is given
	cluster.Spec.Network = &corev1.NetworkSpec{
		MgmtInterface: stringPtr("mgmt-intf"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-m", "mgmt-intf",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Data interface given but empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr("mgmt-intf"),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are empty
	cluster.Spec.Network = &corev1.NetworkSpec{
		DataInterface: stringPtr(""),
		MgmtInterface: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Both data and mgmt interfaces are nil
	cluster.Spec.Network = &corev1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Network spec is nil
	cluster.Spec.Network = &corev1.NetworkSpec{}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithStorageSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-a",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAllWithPartitions
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-A",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// UseAll with UseAllWithPartitions
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:               boolPtr(true),
		UseAllWithPartitions: boolPtr(true),
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// ForceUseDisks
	cluster.Spec.Storage = &corev1.StorageSpec{
		ForceUseDisks: boolPtr(true),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-f",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Journal device
	cluster.Spec.Storage = &corev1.StorageSpec{
		JournalDevice: stringPtr("/dev/journal"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "/dev/journal",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No journal device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		JournalDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.Storage = &corev1.StorageSpec{
		SystemMdDevice: stringPtr("/dev/metadata"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "/dev/metadata",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No metadata device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		SystemMdDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Kvdb device
	cluster.Spec.Storage = &corev1.StorageSpec{
		KvdbDevice: stringPtr("/dev/kvdb"),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-kvdb_dev", "/dev/kvdb",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// No kvdb device if empty
	cluster.Spec.Storage = &corev1.StorageSpec{
		KvdbDevice: stringPtr(""),
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices
	devices := []string{"/dev/one", "/dev/two", "/dev/three"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
		"-s", "/dev/two",
		"-s", "/dev/three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Cache devices
	cacheDevices := []string{"/dev/one", "/dev/two", "/dev/three"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		CacheDevices: &cacheDevices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-cache", "/dev/one",
		"-cache", "/dev/two",
		"-cache", "/dev/three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices empty
	devices = []string{}
	cluster.Spec.Storage = &corev1.StorageSpec{
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAll
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAll:  boolPtr(true),
		Devices: &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Explicit storage devices get priority over UseAllWithPartition
	devices = []string{"/dev/one"}
	cluster.Spec.Storage = &corev1.StorageSpec{
		UseAllWithPartitions: boolPtr(true),
		Devices:              &devices,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "/dev/one",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty storage config
	cluster.Spec.Storage = &corev1.StorageSpec{}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil storage config
	cluster.Spec.Storage = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCloudStorageSpec(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	err := setupMockStorageManager(mockCtrl)
	require.NoError(t, err)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal")},
			},
		},
	}
	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "testNode",
			},
			Status: v1.NodeStatus{
				NodeInfo: v1.NodeSystemInfo{
					OSImage: "Ubuntu 18.04.5 LTS",
				},
			},
		},
	)
	nodeName := "testNode"

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "type=journal",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty journal device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			JournalDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Metadata device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			SystemMdDeviceSpec: stringPtr("type=metadata"),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-metadata", "type=metadata",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty metadata device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			SystemMdDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Kvdb device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			KvdbDeviceSpec: stringPtr("type=kvdb"),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-kvdb_dev", "type=kvdb",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty kvdb device
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			KvdbDeviceSpec: stringPtr(""),
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage device specs
	devices := []string{"type=one", "type=two", "type=three"}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &devices,
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "type=one",
		"-s", "type=two",
		"-s", "type=three",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage device specs empty
	devices = []string{}
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &devices,
		},
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Node pool label
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		NodePoolLabel: "px-storage-type",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-node_pool_label", "px-storage-type",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodes: &maxNodes,
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_drive_set_count", "3",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Storage devices and auto-compute max_storage_nodes_per_zone
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	expectedCloudStorageSpec := []corev1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint64(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint64(210),
			Options: map[string]string{
				"foo3": "bar3",
			}},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "foo1=bar1,iops=110,size=120,type=foo",
		"-s", "foo3=bar3,iops=210,size=220,type=bar",
		"-max_storage_nodes_per_zone", "2",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err := driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Len(t, list, 1)
	assert.ElementsMatch(t, list[0].Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	assert.Equalf(t, nodeName, list[0].Name, "expected node name '%s'", nodeName)

	// Storage devices and use user provided max_storage_nodes_per_zone
	userProvidedInstancesPerZone := 3
	userProvidedInstancesPerZoneUint32 := uint32(userProvidedInstancesPerZone)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
		MaxStorageNodesPerZone: &userProvidedInstancesPerZoneUint32,
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "foo1=bar1,iops=110,size=120,type=foo",
		"-s", "foo3=bar3,iops=210,size=220,type=bar",
		"-max_storage_nodes_per_zone", "2",
	}

	// despite adding a new node, node spec should be pulled from the existing storage node for nodeName
	// Storage node will be created for "nodeName1"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")
	assert.ElementsMatch(t, list[0].Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	// Empty cloud config
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil cloud config
	cluster.Spec.CloudStorage = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// TestCase: Nil cloud config but max storage nodes per zone is set
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone", "2",
	}

	// Empty cloud config
	maxStorageNodesPerZone := uint32(2)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodesPerZone: &maxStorageNodesPerZone,
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// TestCase: Nil cloud config but max storage nodes per zone per node group is set
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_storage_nodes_per_zone_per_nodegroup", "3",
	}

	// Empty cloud config
	maxStorageNodesPerZonePerNodeGroup := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			MaxStorageNodesPerZonePerNodeGroup: &maxStorageNodesPerZonePerNodeGroup,
		},
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 1, len(list), "expected storage nodes in list")

	// Test cloud provider is provided, px version is less than 2.8.0, cloud provider parameter should not be specified.
	cluster.Spec.Image = "portworx/oci-monitor:2.7.0"
	cloudProvider := "AWS"
	cluster.Spec.CloudStorage.Provider = &cloudProvider
	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Test cloud provider is provided, px version is 2.8.0, cloud provider parameter should be specified.
	cluster.Spec.Image = "portworx/oci-monitor:2.8.0"
	expectedArgs = append(expectedArgs, "-cloud_provider", cloudProvider)
	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	cluster.Spec.CloudStorage.Provider = nil
}

func TestPodSpecWithCloudStorageSpecOnEKS(t *testing.T) {
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node1",
				Labels: map[string]string{v1.LabelTopologyZone: "zone1"},
			},
			Spec: v1.NodeSpec{ProviderID: "aws://node-id-1"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node2",
				Labels: map[string]string{v1.LabelTopologyZone: "zone2"},
			},
			Spec: v1.NodeSpec{ProviderID: "aws://node-id-2"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "node3",
				Labels: map[string]string{v1.LabelTopologyZone: "zone3"},
			},
			Spec: v1.NodeSpec{ProviderID: "aws://node-id-3"},
		},
	}}
	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.21.14-eks-ba74326",
	}
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient(fakeK8sNodes)
	err := preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	driver.zoneToInstancesMap, err = cloudprovider.GetZoneMap(k8sClient, "", "")
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Monitoring: &corev1.MonitoringSpec{Telemetry: &corev1.TelemetrySpec{}},
			CloudStorage: &corev1.CloudStorageSpec{
				CapacitySpecs: []corev1.CloudStorageCapacitySpec{
					{
						MinCapacityInGiB: 300,
						Options: map[string]string{
							"foo1": "bar1",
						},
					},
					{
						MinCapacityInGiB: 600,
						Options: map[string]string{
							"foo2": "bar2",
							"type": "io1",
						},
					},
				},
			},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	expectedCapacitySpec := []corev1.CloudStorageCapacitySpec{
		{
			MinCapacityInGiB: 300,
			Options: map[string]string{
				"foo1": "bar1",
				"type": "gp3",
			},
		},
		{
			MinCapacityInGiB: 600,
			Options: map[string]string{
				"foo2": "bar2",
				"type": "io1",
			},
		},
	}
	require.Equal(t, expectedCapacitySpec, cluster.Spec.CloudStorage.CapacitySpecs)
	require.NotNil(t, cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
	require.Equal(t, uint32(1), *cluster.Spec.CloudStorage.MaxStorageNodesPerZone)

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-cloud_provider", "aws",
		"-s", "foo1=bar1,size=100,type=gp3",
		"-s", "foo2=bar2,size=200,type=io1",
		"-max_storage_nodes_per_zone", "1",
		"-secret_type", "k8s",
	}
	actual, _ := driver.GetStoragePodSpec(cluster, fakeK8sNodes.Items[0].Name)

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestPodSpecWithCloudStorageSpecOnGCE(t *testing.T) {
	fakeK8sNodes := &v1.NodeList{Items: []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "node1"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node2"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "node3"}, Spec: v1.NodeSpec{ProviderID: "gce://node-id-3"}},
	}}

	versionClient := fakek8sclient.NewSimpleClientset(fakeK8sNodes)
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.26.5-gke.1200",
	}
	coreops.SetInstance(coreops.New(versionClient))
	k8sClient := testutil.FakeK8sClient(fakeK8sNodes)
	err := preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)

	require.True(t, preflight.IsGKE())
	c := preflight.Instance()
	require.Equal(t, cloudops.GCE, c.ProviderName())

	driver := portworx{}
	err = driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image:        "portworx/oci-monitor:3.0.0",
			CloudStorage: &corev1.CloudStorageSpec{},
		},
	}

	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-b",
		"-cloud_provider", "gce",
		"-max_storage_nodes_per_zone", "3",
		"-secret_type", "k8s",
	}
	actual, _ := driver.GetStoragePodSpec(cluster, fakeK8sNodes.Items[0].Name)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Reset preflight for other tests
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err = preflight.InitPreflightChecker(k8sClient)
	require.NoError(t, err)
}

func TestPodSpecWithCapacitySpecsAndDeviceSpecs(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	err := setupMockStorageManager(mockCtrl)
	require.NoError(t, err)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
		},
	}

	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "testNode",
			},
			Status: v1.NodeStatus{
				NodeInfo: v1.NodeSystemInfo{
					OSImage: "Ubuntu 18.04.5 LTS",
				},
			},
		},
	)

	// Provide both devices specs and cloud capacity specs
	deviceSpecs := []string{"type=one", "type=two"}

	nodeName := "testNode"

	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CloudStorageCommon: corev1.CloudStorageCommon{
			DeviceSpecs: &deviceSpecs,
		},
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "foo1=bar1,iops=110,size=120,type=foo",
		"-s", "foo3=bar3,iops=210,size=220,type=bar",
		"-max_storage_nodes_per_zone", "2",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

}

func TestPodSpecWithStorageAndCloudStorageSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					JournalDevice: stringPtr("/dev/journal"),
				},
			},
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal"),
				},
			},
		},
	}
	driver := portworx{}

	// Use storage spec over cloud storage spec if not empty
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-j", "/dev/journal",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithSecretsProvider(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			SecretsProvider: stringPtr("k8s"),
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-secret_type", "k8s",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if empty
	cluster.Spec.SecretsProvider = stringPtr("")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the secrets provider if nil
	cluster.Spec.SecretsProvider = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithCustomStartPort(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	startPort := uint32(10001)

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image:     "portworx/oci-monitor:2.1.1",
			StartPort: &startPort,
		},
	}
	driver := portworx{}

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/portworxPodCustomPort.yaml")

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Don't set the start port if same as default start port
	startPort = uint32(pxutil.DefaultStartPort)
	cluster.Spec.StartPort = &startPort
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Don't set the start port if nil
	cluster.Spec.StartPort = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithDNSPolicy(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "px-cluster",
			Namespace:   "kube-system",
			Annotations: map[string]string{},
		},
	}
	driver := portworx{}

	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.DNSPolicy, v1.DNSPolicy(""))

	cluster.Annotations[pxutil.AnnotationDNSPolicy] = string(v1.DNSClusterFirstWithHostNet)
	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.DNSPolicy, v1.DNSClusterFirstWithHostNet)

	cluster.Annotations[pxutil.AnnotationDNSPolicy] = string(v1.DNSClusterFirst)
	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.DNSPolicy, v1.DNSClusterFirst)
}

func TestPodSpecWithHostPid(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationHostPid: "true",
			},
		},
	}
	driver := portworx{}

	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.HostPID, true)

	cluster.Annotations[pxutil.AnnotationHostPid] = "false"
	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.HostPID, false)

	cluster.Annotations[pxutil.AnnotationHostPid] = "invalid"
	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Equal(t, podSpec.HostPID, false)
}

func TestPodSpecWithLogAnnotation(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationLogFile: "/tmp/log",
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--log", "/tmp/log",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithRuntimeOptions(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				RuntimeOpts: map[string]string{
					"key": "10",
				},
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-rt_opts", "key=10",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// For invalid value key should not be added. Only numeric values are supported.
	cluster.Spec.RuntimeOpts["invalid"] = "non-numeric"

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Empty runtime map should not add any options
	cluster.Spec.RuntimeOpts = make(map[string]string)
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// Nil runtime map should not add any options
	cluster.Spec.RuntimeOpts = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "-fruit apple -person john",
			},
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-fruit", "apple",
		"-person", "john",
	}

	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithInvalidMiscArgs(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "'-unescaped-quote",
			},
		},
	}
	driver := portworx{}

	// Don't add misc args if they are invalid
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithEssentials(t *testing.T) {
	defer func() {
		os.Unsetenv(pxutil.EnvKeyPortworxEssentials)
		os.Unsetenv(pxutil.EnvKeyMarketplaceName)
	}()
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationMiscArgs: "--oem custom",
			},
		},
	}
	driver := portworx{}

	// TestCase: Replace existing oem value to use essentials
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "true")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not change anything if oem is already essentials
	cluster.Annotations[pxutil.AnnotationMiscArgs] = "--oem esse"

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc args are empty
	cluster.Annotations[pxutil.AnnotationMiscArgs] = ""

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc arg annotation not present
	cluster.Annotations = nil

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem flag if misc arg does not have oem flag
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1 v1",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k1", "v1",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Add oem value even if existing oem arg is invalid
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1 v1 --oem ",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Update oem value even if in the middle
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "-k1  v1  --oem  custom  -k2  v2",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-k1", "v1",
		"--oem", "esse",
		"-k2", "v2",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is present
	os.Setenv(pxutil.EnvKeyMarketplaceName, "operatorhub")
	cluster.Annotations = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
		"-marketplace_name", "operatorhub",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name should not be passed for older portworx releases
	cluster.Spec.Image = "image/portworx:2.5.4"
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name should be passed for newer portworx releases
	cluster.Spec.Image = "image/portworx:2.5.5"
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
		"-marketplace_name", "operatorhub",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is empty
	os.Setenv(pxutil.EnvKeyMarketplaceName, "")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Marketplace name is empty string on trimming
	os.Setenv(pxutil.EnvKeyMarketplaceName, "    ")
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "esse",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not update oem value if essentials is disabled
	os.Setenv(pxutil.EnvKeyPortworxEssentials, "false")
	os.Setenv(pxutil.EnvKeyMarketplaceName, "operatorhub")
	cluster.Annotations = map[string]string{
		pxutil.AnnotationMiscArgs: "--oem custom",
	}
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--oem", "custom",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)

	// TestCase: Do not add oem value if essentials is disabled
	cluster.Annotations = nil
	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecWithImagePullPolicy(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image:           "portworx/oci-monitor:2.12.0",
			ImagePullPolicy: v1.PullIfNotPresent,
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "csi/registrar",
			},
		},
	}
	driver := portworx{}

	// Pass the image pull policy in the pod args
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"--pull", "IfNotPresent",
	}

	// Case 1: When portworx version is lesser than 2.13
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Len(t, actual.Containers, 2)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[0].ImagePullPolicy)
	assert.Equal(t, v1.PullIfNotPresent, actual.Containers[1].ImagePullPolicy)

	// Case 2: When portworx version is greater than or equal to 2.13 then csi-node-driver-registrar container will be a part of portworx-api daemonset
	// Hence only 1 container should be present in the storage pods
	cluster.Spec.Image = "portworx/oci-monitor:2.13.0"
	newActualSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, newActualSpec.Containers[0].Args)
	assert.Len(t, newActualSpec.Containers, 1)
	assert.Equal(t, v1.PullIfNotPresent, newActualSpec.Containers[0].ImagePullPolicy)

}

func TestPodSpecWithNilStorageCluster(t *testing.T) {
	var cluster *corev1.StorageCluster
	driver := portworx{}
	nodeName := "testNode"

	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPodSpecWithInvalidKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
	}
	driver := portworx{}

	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPKSPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/pks.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
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
										Key:      "kubernetes.io/os",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"linux"},
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
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// check PKS when using px-2.9.1

	cluster.Spec.Image = "portworx/oci-monitor:2.9.1"
	expected_2_9_1 := expected.DeepCopy()
	expected_2_9_1.Containers[0].Image = "docker.io/" + cluster.Spec.Image
	podSpecAddMount(expected_2_9_1, "varlibosd", "/var/lib/osd:/var/lib/osd:shared")
	podSpecRemoveEnv(expected_2_9_1, "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS")

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected_2_9_1, &actual)

	// check PKS when using px-3.0.2

	cluster.Spec.Image = "portworx/oci-monitor:3.0.2"
	expected_3_0_2 := expected_2_9_1.DeepCopy()
	expected_3_0_2.Containers[0].Image = "docker.io/" + cluster.Spec.Image
	expected_3_0_2.Containers[0].Args = append(expected_3_0_2.Containers[0].Args,
		"-v", "/var/lib/osd/pxns:/var/lib/osd/pxns:shared",
		"-v", "/var/lib/osd/mounts:/var/lib/osd/mounts:shared",
	)
	podSpecRemoveMount(expected_3_0_2, "varlibosd")
	podSpecRemoveMount(expected_3_0_2, "pxlogs")
	podSpecAddMount(expected_3_0_2, "varlibosd", "/var/vcap/store/lib/osd:/var/lib/osd")
	podSpecRemoveEnv(expected_3_0_2, "PRE-EXEC")
	expected_3_0_2.Containers[0].Env = append(expected_3_0_2.Containers[0].Env, v1.EnvVar{
		Name: "PRE-EXEC", Value: "rm -fr /var/lib/osd/driver",
	})

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected_3_0_2, &actual)
}

func TestOpenshiftRuncPodSpec(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/openshift_runc.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsOpenshift: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			Placement: &corev1.PlacementSpec{
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
										Key:      "kubernetes.io/os",
										Operator: v1.NodeSelectorOpIn,
										Values:   []string{"linux"},
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
			Kvdb: &corev1.KvdbSpec{
				Internal: true,
			},
			SecretsProvider: stringPtr("k8s"),
			CommonConfig: corev1.CommonConfig{
				Storage: &corev1.StorageSpec{
					UseAll: boolPtr(true),
				},
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Test securityContxt w/ privileged=false
	cluster.ObjectMeta.Annotations[pxutil.AnnotationIsPrivileged] = "false"

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need portworx higher than 3.0.1 to use annotation '"+
		pxutil.AnnotationIsPrivileged+"'")

	cluster.Spec.Image = "portworx/oci-monitor:3.0.2"
	expected_3_0_2 := expected.DeepCopy()
	expected_3_0_2.Containers[0].Image = "docker.io/" + cluster.Spec.Image
	expected_3_0_2.Containers[0].Args = append(expected_3_0_2.Containers[0].Args,
		"-v", "/var/lib/osd/pxns:/var/lib/osd/pxns:shared",
		"-v", "/var/lib/osd/mounts:/var/lib/osd/mounts:shared",
	)
	expected_3_0_2.Containers[0].SecurityContext = &v1.SecurityContext{
		Privileged: boolPtr(false),
		Capabilities: &v1.Capabilities{
			Add: []v1.Capability{
				"SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE", "SYS_RAWIO", "SYS_MODULE", "LINUX_IMMUTABLE",
			},
		},
	}
	podSpecAddMount(expected_3_0_2, "varlibosd", "/var/lib/osd:/var/lib/osd")
	podSpecRemoveEnv(expected_3_0_2, "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS")

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	assertPodSpecEqual(t, expected_3_0_2, &actual)

	require.Equal(t, 1, len(actual.Containers))
	for _, v := range actual.Containers[0].VolumeMounts {
		assert.Nil(t, v.MountPropagation, "Wrong propagation on %v", v)
	}

	// PWX-32825 tweak the 3.0.2 version slightly -- should still evaluate the same
	cluster.Spec.Image = "portworx/oci-monitor:3.0.2-ubuntu1604"
	expected_3_0_2.Containers[0].Image = "docker.io/" + cluster.Spec.Image

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	assertPodSpecEqual(t, expected_3_0_2, &actual)
}

func TestPodSpecForK3s(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4+k3s1",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_k3s.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected, &actual)

	// retry w/ RKE2 version identifier0 -- should also default to K3s distro tweaks
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.21.4+rke2r2",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForBottleRocketAMI(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.20.1",
	}

	nodeName := "testNode"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.7.1",
		},
	}
	driver := portworx{
		k8sClient: testutil.FakeK8sClient(
			&v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeName,
					Namespace: "",
				},
				Status: v1.NodeStatus{
					NodeInfo: v1.NodeSystemInfo{
						OSImage: "BottleRocket OS 123",
					},
				},
			},
		),
	}

	// we'll be expecting BottleRocket-specific args
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-disable-log-proxy",
		"--install-uncompress",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Len(t, actual.Containers, 1)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Contains(t, actual.Volumes, v1.Volume{
		Name: "containerd-br",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: "/run/dockershim.sock",
			},
		},
	})
	// also double-check for BottleRocket-specific mount
	assert.Contains(t, actual.Containers[0].VolumeMounts, v1.VolumeMount{
		Name:      "containerd-br",
		MountPath: "/run/containerd/containerd.sock",
	})

	// double-check permissions
	expectedCapabilities := []v1.Capability{
		"SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE", "SYS_RAWIO", "SYS_MODULE", "LINUX_IMMUTABLE",
	}
	require.NotNil(t, actual.Containers[0].SecurityContext.Privileged)
	assert.False(t, *actual.Containers[0].SecurityContext.Privileged)

	require.NotNil(t, actual.Containers[0].SecurityContext.Capabilities)
	assert.ElementsMatch(t, expectedCapabilities, actual.Containers[0].SecurityContext.Capabilities.Add)

	require.NotNil(t, actual.SecurityContext)
	require.NotNil(t, actual.SecurityContext.SELinuxOptions)
	assert.Equal(t, "super_t", actual.SecurityContext.SELinuxOptions.Type)
}

func TestPodWithTelemetry(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry-with-location.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				"portworx.io/arcus-location": "internal",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.8.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "portworx/px-telemetry:2.1.2",
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
		},
	}
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected, &actual)

	// don't specify arcus location
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry.yaml")
	delete(cluster.Annotations, "portworx.io/arcus-location")
	driver = portworx{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Add a proxy for CCM service
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry_with_proxy.yaml")
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  pxutil.EnvKeyPortworxHTTPProxy,
			Value: "https://username:password@hotstname:port",
		},
	}
	driver = portworx{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Remove the proxy for CCM service
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry.yaml")
	cluster.Spec.Env = nil
	driver = portworx{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Now disable telemetry
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_disable_telemetry.yaml")
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	driver = portworx{}
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodWithTelemetryUpgrade(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_telemetry.yaml")
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.8.0",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "portworx/px-telemetry:2.1.2",
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
		},
	}
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	createTelemetrySecret(t, k8sClient, cluster.Namespace)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assertPodSpecEqual(t, expected, &actual)

	// Upgrade px version, ccm should upgrade and reset telemetry image specified, ccm container should be removed
	cluster.Spec.Image = "portworx/oci-monitor:2.12.0"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	require.Empty(t, cluster.Spec.Monitoring.Telemetry.Image)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Equal(t, len(expected.Containers)-1, len(actual.Containers))

	// Disable telemetry
	expected = getExpectedPodSpecFromDaemonset(t, "testspec/px_disable_telemetry.yaml")
	cluster.Spec.Monitoring.Telemetry.Enabled = false
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	require.Equal(t, len(expected.Containers), len(actual.Containers))
}

func TestPodWithTelemetryCCMVolume(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.18.4",
	}
	fakeExtClient := fakeextclient.NewSimpleClientset()
	apiextensionsops.SetInstance(apiextensionsops.New(fakeExtClient))
	k8sClient := testutil.FakeK8sClient()
	nodeName := "testNode"

	// Start with older version of portworx
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.13.7",
			Monitoring: &corev1.MonitoringSpec{
				Telemetry: &corev1.TelemetrySpec{
					Enabled: true,
					Image:   "portworx/px-telemetry:2.1.2",
				},
			},
			CSI: &corev1.CSISpec{
				Enabled: false,
			},
		},
	}
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	createTelemetrySecret(t, k8sClient, cluster.Namespace)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	podSpec, err := driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	hasCCMVol := false
	hasCCMVolMount := false

	for _, vol := range podSpec.Volumes {
		if vol.Name == "ccm-phonehome-config" {
			hasCCMVol = true
		}
	}
	for _, ctn := range podSpec.Containers {
		for _, volMnt := range ctn.VolumeMounts {
			if volMnt.Name == "ccm-phonehome-config" {
				hasCCMVolMount = true
			}
		}
	}
	// The pod spec should neither contain ccm volume nor volume mount
	assert.False(t, hasCCMVol || hasCCMVolMount)

	// Then upgrade the cluster to 3.0 and obtain the new pod spec
	cluster.Spec.Image = "portworx/oci-monitor:2.13.8"
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	podSpec, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)

	for _, vol := range podSpec.Volumes {
		if vol.Name == "ccm-phonehome-config" {
			hasCCMVol = true
		}
	}
	for _, ctn := range podSpec.Containers {
		for _, volMnt := range ctn.VolumeMounts {
			if volMnt.Name == "ccm-phonehome-config" {
				hasCCMVolMount = true
			}
		}
	}
	// Now the pod spec should contain both ccm volume and volume mount
	assert.True(t, hasCCMVol && hasCCMVolMount)
}

func TestPodSpecWhenRunningOnMasterEnabled(t *testing.T) {
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_master.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationRunOnMaster: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}
	driver := portworx{}
	err := driver.Init(k8sClient, runtime.NewScheme(), record.NewFakeRecorder(100))
	require.NoError(t, err)
	err = driver.SetDefaultsOnStorageCluster(cluster)
	require.NoError(t, err)

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForCSIWithOlderCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	// Should use 0.3 csi version for k8s version less than 1.13
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.12.8",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_csi_0.3.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSIDriverRegistrar: "quay.io/k8scsi/driver-registrar:v0.4.2",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Update Portworx version, which should use new CSI driver name
	cluster.Spec.Image = "portworx/oci-monitor:2.2"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[3],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithNewerCSIVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.2",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_csi_1.0.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)

	// Update Portworx version, which should use new CSI driver name
	cluster.Spec.Image = "portworx/oci-monitor:2.2"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithCustomPortworxImage(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}

	// PX_IMAGE env var gets precedence over spec.image.
	// We verify that by checking that CSI registrar is using old driver name.
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PX_IMAGE",
						Value: "portworx/oci-monitor:2.1.1-rc1",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If version cannot be found from the Portworx image tag, then check the annotation
	// for version. This is useful in testing when your image tag does not have version.
	cluster.Spec.Image = "portworx/oci-monitor:custom_oci_tag"
	cluster.Spec.Env[0].Value = "portworx/oci-monitor:custom_px_tag"
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPXVersion: "2.1",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If valid version is not found from the image or the annotation, then assume latest
	// Portworx version. Verify this by checking there is no csi-registrar container
	cluster.Annotations = map[string]string{
		pxutil.AnnotationPXVersion: "portworx/oci-monitor:invalid",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.Equal(t, len(actual.Containers), 1)
	assert.Equal(t, actual.Containers[0].Name, "portworx")
}

func TestPodSpecForDeprecatedCSIDriverName(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.13.0",
	}

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is true, use the old CSI driver name.
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.2",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  "PORTWORX_USEDEPRECATED_CSIDRIVERNAME",
						Value: "true",
					},
				},
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}
	nodeName := "testNode"

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME has true value, use old CSI driver name.
	cluster.Spec.Env[0].Value = "1"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/com.openstorage.pxd/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is false, use new CSI driver name.
	cluster.Spec.Env[0].Value = "false"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)

	// If PORTWORX_USEDEPRECATED_CSIDRIVERNAME is invalid, use new CSI driver name.
	cluster.Spec.Env[0].Value = "invalid_boolean"
	driver = portworx{}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err)

	assert.Equal(t,
		actual.Containers[1].Args[2],
		"--kubelet-registration-path=/var/lib/kubelet/csi-plugins/pxd.portworx.com/csi.sock",
	)
}

func TestPodSpecForCSIWithIncorrectKubernetesVersion(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "invalid-version",
	}

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
	}

	driver := portworx{}
	_, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.Error(t, err, "Expected an error on GetStoragePodSpec")
}

func TestPodSpecForKvdbAuthCerts(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbCA:       []byte("kvdb-ca-file"),
				SecretKeyKvdbCert:     []byte("kvdb-cert-file"),
				SecretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				SecretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				SecretKeyKvdbUsername: []byte("kvdb-username"),
				SecretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForPKSWithCSI(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.15.2",
	}
	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_pks_with_csi.yaml")

	nodeName := "testNode"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationIsPKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.5.5",
			CSI: &corev1.CSISpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				CSINodeDriverRegistrar: "quay.io/k8scsi/csi-node-driver-registrar:v1.1.0",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutCert(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbCA:       []byte("kvdb-ca-file"),
				SecretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				SecretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				SecretKeyKvdbUsername: []byte("kvdb-username"),
				SecretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_cert.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutCA(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbCert:     []byte("kvdb-cert-file"),
				SecretKeyKvdbCertKey:  []byte("kvdb-key-file"),
				SecretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				SecretKeyKvdbUsername: []byte("kvdb-username"),
				SecretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_ca.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAuthCertsWithoutKey(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbCA:       []byte("kvdb-ca-file"),
				SecretKeyKvdbCert:     []byte("kvdb-cert-file"),
				SecretKeyKvdbACLToken: []byte("kvdb-acl-token"),
				SecretKeyKvdbUsername: []byte("kvdb-username"),
				SecretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_certs_without_key.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				Endpoints:  []string{"ep1", "ep2", "ep3"},
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestPodSpecForKvdbAclToken(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbACLToken: []byte("kvdb-acl-token"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-acltoken", "kvdb-acl-token",
	}

	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)
	assert.ElementsMatch(t, expected.Containers[0].VolumeMounts, actual.Containers[0].VolumeMounts)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecForKvdbUsernamePassword(t *testing.T) {
	fakeClient := fakek8sclient.NewSimpleClientset(
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kvdb-auth-secret",
				Namespace: "kube-test",
			},
			Data: map[string][]byte{
				SecretKeyKvdbUsername: []byte("kvdb-username"),
				SecretKeyKvdbPassword: []byte("kvdb-password"),
			},
		},
	)
	coreops.SetInstance(coreops.New(fakeClient))

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-userpwd", "kvdb-username:kvdb-password",
	}

	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)
	assert.ElementsMatch(t, expected.Containers[0].VolumeMounts, actual.Containers[0].VolumeMounts)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func TestPodSpecForKvdbAuthErrorReadingSecret(t *testing.T) {
	// Create fake client without kvdb auth secret
	fakeClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(fakeClient))

	expected := getExpectedPodSpecFromDaemonset(t, "testspec/px_kvdb_without_certs.yaml")

	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.1.1",
			Kvdb: &corev1.KvdbSpec{
				AuthSecret: "kvdb-auth-secret",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assertPodSpecEqual(t, expected, &actual)
}

func TestIfStorageNodeExists(t *testing.T) {
	testCases := []struct {
		in       []*corev1.StorageNode
		nodeName string
		result   bool
	}{
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "1",
			result:   true,
		},
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "5",
			result:   false,
		},
		{
			in: []*corev1.StorageNode{
				{ObjectMeta: metav1.ObjectMeta{Name: "1"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "2"}},
			},
			nodeName: "",
			result:   false,
		},
	}
	for _, tc := range testCases {
		assert.Equal(t, storageNodeExists(tc.nodeName, tc.in), tc.result)
	}
}

func TestStorageNodeConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	err := setupMockStorageManager(mockCtrl)
	require.NoError(t, err)

	_, yamlData := generateValidYamlData(t)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			UID:       "px-cluster-UID",
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
			CloudStorage: &corev1.CloudStorageSpec{
				CloudStorageCommon: corev1.CloudStorageCommon{
					JournalDeviceSpec: stringPtr("type=journal")},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: "kube-system",
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
		cluster,
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode"}},
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode2"}},
		&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "testNode3"}},
	)
	nodeName := "testNode"
	nodeName2 := "testNode2"
	nodeName3 := "testNode3"

	zoneToInstancesMap := map[string]uint64{"a": 3, "b": 3, "c": 2}
	driver := portworx{
		k8sClient:          k8sClient,
		recorder:           record.NewFakeRecorder(0),
		zoneToInstancesMap: zoneToInstancesMap,
		cloudProvider:      "mock",
	}

	// Max storage nodes
	maxNodes := uint32(3)
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		MaxStorageNodes: &maxNodes,
	}
	expectedArgs := []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-max_drive_set_count", "3",
	}

	// TC 1, no CloudStorage CapacitySpecs
	actual, _ := driver.GetStoragePodSpec(cluster, nodeName)
	list, err := driver.storageNodesList(cluster)
	assert.NoError(t, err, "Error getting StorageNodeList")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, 0, len(list), "no storage nodes are expected at this point")

	// Storage devices and auto-compute max_storage_nodes_per_zone
	cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{
		CapacitySpecs: []corev1.CloudStorageCapacitySpec{
			{
				MinIOPS:          uint64(100),
				MinCapacityInGiB: uint64(100),
				MaxCapacityInGiB: uint64(200),
				Options:          map[string]string{"foo1": "bar1"},
			},
			{
				MinIOPS:          uint64(200),
				MinCapacityInGiB: uint64(200),
				MaxCapacityInGiB: uint64(500),
				Options:          map[string]string{"foo3": "bar3"},
			},
		},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "foo1=bar1,iops=110,size=120,type=foo",
		"-s", "foo3=bar3,iops=210,size=220,type=bar",
		"-max_storage_nodes_per_zone", "2",
	}

	inputInstancesPerZone := uint64(2)
	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        uint64(len(zoneToInstancesMap)),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint64(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint64(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint64(210),
				},
			},
		}, nil)

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, len(list), 1, "expected storage nodes in list")

	expectedCloudStorageSpec := []corev1.StorageNodeCloudDriveConfig{
		{
			Type:      "foo",
			SizeInGiB: uint64(120),
			IOPS:      uint64(110),
			Options:   map[string]string{"foo1": "bar1"},
		},
		{
			Type:      "bar",
			SizeInGiB: uint64(220),
			IOPS:      uint64(210),
			Options: map[string]string{
				"foo3": "bar3",
			}},
	}

	expectedArgs = []string{
		"-c", "px-cluster",
		"-x", "kubernetes",
		"-s", "foo1=bar1,iops=110,size=120,type=foo",
		"-s", "foo3=bar3,iops=210,size=220,type=bar",
		"-max_storage_nodes_per_zone", "2",
	}

	// despite adding a new node, node spec should be pulled from the existing storage node for nodeName
	// Storage node will be created for "nodeName1"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName2)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 2, len(list), "expected storage nodes in list")
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}

	actual, _ = driver.GetStoragePodSpec(cluster, nodeName3)
	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
	list, err = driver.storageNodesList(cluster)
	assert.NoError(t, err, "Unexpected error on GetStorageNodeList")
	assert.Equal(t, 3, len(list), "expected storage nodes in list")
	assert.Equal(t, uint64(inputInstancesPerZone), cluster.Status.Storage.StorageNodesPerZone, "wrong number of instances per zone in a the cluster status")

	for _, node := range list {
		assert.ElementsMatch(t, node.Spec.CloudStorage.DriveConfigs, expectedCloudStorageSpec)
	}
}

func TestSecuritySetEnv(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
			},
		},
	}
	validateSecuritySetEnv(t, cluster)

	// security enabled, auth enabled explicitly
	cluster = &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(true),
				},
			},
		},
	}
	validateSecuritySetEnv(t, cluster)
}

func validateSecuritySetEnv(t *testing.T, cluster *corev1.StorageCluster) {
	k8sClient := coreops.New(fakek8sclient.NewSimpleClientset())
	coreops.SetInstance(k8sClient)
	nodeName := "testNode"

	setSecuritySpecDefaults(cluster)

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	expectedJwtIssuer := "operator.portworx.io"
	var jwtIssuer string
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthJwtIssuer {
			jwtIssuer = env.Value
			break
		}
	}
	assert.Equal(t, expectedJwtIssuer, jwtIssuer)

	expectedJwtIssuer = "test.io"
	cluster.Spec.Security.Auth.SelfSigned.Issuer = stringPtr(expectedJwtIssuer)
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	var sharedSecretSet bool
	var systemSecretSet bool
	var appsSecretSet bool
	var storkSecretSet bool
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthJwtIssuer {
			jwtIssuer = env.Value
		}
		if env.Name == pxutil.EnvKeyPortworxAuthJwtSharedSecret {
			sharedSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthSystemKey {
			systemSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.Equal(t, expectedJwtIssuer, jwtIssuer)
	assert.True(t, sharedSecretSet)
	assert.True(t, systemSecretSet)
	assert.True(t, appsSecretSet)
	assert.False(t, storkSecretSet)

	// for px < 2.6, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.5.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork < 2.5 and px 2.6+, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:2.3.2",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork < 2.5 and px 2.6+ with annotation, use stork secret env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Annotations = make(map[string]string)
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "2.3.2"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	storkSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthStorkKey {
			storkSecretSet = true
		}
	}
	assert.True(t, storkSecretSet)

	// for stork >= 2.5 and px 2.6+, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:2.5.0",
		Enabled: true,
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, appsSecretSet)

	// for desired image stork >= 2.5 and px 2.6+, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: true,
	}
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Stork: "openstorage/image:2.5.0",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, appsSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "2.5.0"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, appsSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image:abcde2_12313a",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "openstorage/image:abcde2_465313a"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, appsSecretSet)

	// for stork >= 2.5 and px 2.6+ with annotation, use apps issuer env var
	cluster.Spec.Image = "portworx/image:2.6.0"
	cluster.Spec.Stork = &corev1.StorkSpec{
		Image:   "openstorage/image",
		Enabled: true,
	}
	cluster.Annotations[pxutil.AnnotationStorkVersion] = "openstorage/image"
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")
	appsSecretSet = false
	for _, env := range actual.Containers[0].Env {
		if env.Name == pxutil.EnvKeyPortworxAuthSystemAppsKey {
			appsSecretSet = true
		}
	}
	assert.True(t, appsSecretSet)
}

func TestPodSpecForIKS(t *testing.T) {
	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				pxutil.AnnotationIsIKS: "true",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.6.0",
		},
	}

	expectedPodIPEnv := v1.EnvVar{
		Name: "PX_POD_IP",
		ValueFrom: &v1.EnvVarSource{
			FieldRef: &v1.ObjectFieldSelector{
				APIVersion: "v1",
				FieldPath:  "status.podIP",
			},
		},
	}

	driver := portworx{}
	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.Containers[0].Env, 5)
	var podIPEnv *v1.EnvVar
	for _, env := range actual.Containers[0].Env {
		if env.Name == "PX_POD_IP" {
			podIPEnv = env.DeepCopy()
		}
	}
	assert.Equal(t, expectedPodIPEnv, *podIPEnv)

	// ensure additional volumes have been added
	vol := v1.Volume{
		Name: "cripersistentstorage-iks",
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: "/var/data/cripersistentstorage",
			},
		},
	}
	volMnt := v1.VolumeMount{
		Name:      "cripersistentstorage-iks",
		MountPath: "/var/data/cripersistentstorage",
	}
	assert.Contains(t, actual.Volumes, vol)
	assert.Contains(t, actual.Containers[0].VolumeMounts, volMnt)

	// TestCase: When IKS is not enabled
	cluster.Annotations[pxutil.AnnotationIsIKS] = "false"

	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.Len(t, actual.Containers[0].Env, 4)
	podIPEnv = nil
	for _, env := range actual.Containers[0].Env {
		if env.Name == "PX_POD_IP" {
			podIPEnv = env.DeepCopy()
		}
	}
	assert.Nil(t, podIPEnv)
	assert.NotContains(t, actual.Volumes, vol)
	assert.NotContains(t, actual.Containers[0].VolumeMounts, volMnt)

	// now that the annotation is off, check if we can activate IKS via Kubernets version
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.25.8+IKS",
	}
	actual, err = driver.GetStoragePodSpec(cluster, nodeName)
	require.NoError(t, err)
	assert.Contains(t, actual.Volumes, vol)
	assert.Contains(t, actual.Containers[0].VolumeMounts, volMnt)
}

func TestPruneVolumes(t *testing.T) {
	spec := v1.PodSpec{
		Volumes: []v1.Volume{
			{Name: "foo"},
			{Name: "bar"},
			{Name: "foo-override"},
			{Name: "orphaned"},
		},
		Containers: []v1.Container{
			{
				Name: "containerA",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
					{
						Name:      "bar",
						MountPath: "/mnt/bar",
					},
				},
			},
			{
				Name: "containerB",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
					{
						Name:      "bar",
						MountPath: "/mnt/bar",
					},
					{
						Name:      "foo-override",
						MountPath: "/mnt/foo",
					},
				},
			},
			{
				Name: "containerC",
				VolumeMounts: []v1.VolumeMount{
					{
						Name:      "foo",
						MountPath: "/mnt/foo/",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo////",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo/bar/..",
					},
					{
						Name:      "foo",
						MountPath: "/mnt/foo",
					},
				},
			},
		},
	}

	testObj := &portworx{}
	testObj.pruneVolumes(&spec)

	expectedVolumes := []v1.Volume{
		{Name: "foo"},
		{Name: "bar"},
		{Name: "foo-override"},
	}
	assert.Equal(t, expectedVolumes, spec.Volumes)

	expectedMounts := []v1.VolumeMount{
		{
			Name:      "foo",
			MountPath: "/mnt/foo",
		},
		{
			Name:      "bar",
			MountPath: "/mnt/bar",
		},
	}
	require.Equal(t, "containerA", spec.Containers[0].Name)
	assert.Equal(t, expectedMounts, spec.Containers[0].VolumeMounts)

	expectedMounts = []v1.VolumeMount{
		{
			Name:      "bar",
			MountPath: "/mnt/bar",
		},
		{
			Name:      "foo-override",
			MountPath: "/mnt/foo",
		},
	}
	require.Equal(t, "containerB", spec.Containers[1].Name)
	assert.Equal(t, expectedMounts, spec.Containers[1].VolumeMounts)

	expectedMounts = []v1.VolumeMount{
		{
			Name:      "foo",
			MountPath: "/mnt/foo",
		},
	}
	require.Equal(t, "containerC", spec.Containers[2].Name)
	assert.Equal(t, expectedMounts, spec.Containers[2].VolumeMounts)
}

func TestPodSpecWithClusterIDOverwritten(t *testing.T) {
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	nodeName := "testNode"

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
			Annotations: map[string]string{
				pxutil.AnnotationClusterID: "cluster-id-overwritten",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Image: "portworx/oci-monitor:2.0.3.4",
		},
	}
	driver := portworx{}

	expectedArgs := []string{
		"-c", "cluster-id-overwritten",
		"-x", "kubernetes",
	}

	actual, err := driver.GetStoragePodSpec(cluster, nodeName)
	assert.NoError(t, err, "Unexpected error on GetStoragePodSpec")

	assert.ElementsMatch(t, expectedArgs, actual.Containers[0].Args)
}

func getExpectedPodSpecFromDaemonset(t *testing.T, fileName string) *v1.PodSpec {
	json, err := os.ReadFile(fileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	ds, ok := obj.(*v1beta1.DaemonSet)
	require.True(t, ok, "Expected daemon set object, got %T", obj)

	return &ds.Spec.Template.Spec
}

func getExpectedPodSpec(t *testing.T, podSpecFileName string) *v1.PodSpec {
	json, err := os.ReadFile(podSpecFileName)
	assert.NoError(t, err)

	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(json), nil, nil)
	assert.NoError(t, err)

	p, ok := obj.(*v1.Pod)
	assert.True(t, ok, "Expected pod object")

	return &p.Spec
}

func assertPodSpecEqual(t *testing.T, expected, actual *v1.PodSpec) {
	assert.Equal(t, expected.Affinity, actual.Affinity)
	assert.Equal(t, expected.HostNetwork, actual.HostNetwork)
	assert.Equal(t, expected.RestartPolicy, actual.RestartPolicy)
	assert.Equal(t, expected.ServiceAccountName, actual.ServiceAccountName)
	assert.Equal(t, expected.ImagePullSecrets, actual.ImagePullSecrets)
	assert.Equal(t, expected.Tolerations, actual.Tolerations)
	assert.ElementsMatch(t, expected.Volumes, actual.Volumes)

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
	assert.ElementsMatch(t, expected.Args, actual.Args)
	assert.ElementsMatch(t, expected.Env, actual.Env)
	assert.ElementsMatch(t, expected.VolumeMounts, actual.VolumeMounts)
}

func podSpecAddMount(ps *v1.PodSpec, name, mntSpec string) {
	if ps == nil || name == "" || mntSpec == "" {
		return
	}
	parts := strings.Split(mntSpec, ":")
	src := v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: parts[0],
			},
		},
	}
	dst := v1.VolumeMount{
		Name:      name,
		MountPath: parts[1],
	}
	if len(parts) > 2 {
		if parts[2] == "shared" || parts[2] == "rshared" {
			dst.MountPropagation = mountPropagationModePtr(v1.MountPropagationBidirectional)
		}
	}
	ps.Volumes = append(ps.Volumes, src)
	ps.Containers[0].VolumeMounts = append(ps.Containers[0].VolumeMounts, dst)
}

func podSpecRemoveMount(ps *v1.PodSpec, name string) {
	if ps == nil || name == "" {
		return
	}

	newVols := make([]v1.Volume, 0, len(ps.Volumes))
	for _, v := range ps.Volumes {
		if v.Name == name {
			continue
		}
		newVols = append(newVols, v)
	}
	ps.Volumes = newVols

	newVolMmounts := make([]v1.VolumeMount, 0, len(ps.Containers[0].VolumeMounts))
	for _, v := range ps.Containers[0].VolumeMounts {
		if v.Name == name {
			continue
		}
		newVolMmounts = append(newVolMmounts, v)
	}
	ps.Containers[0].VolumeMounts = newVolMmounts
}

func podSpecRemoveEnv(ps *v1.PodSpec, name string) {
	if ps == nil || name == "" {
		return
	}

	newEnv := make([]v1.EnvVar, 0, len(ps.Containers[0].Env))
	for _, v := range ps.Containers[0].Env {
		if v.Name == name {
			continue
		}
		newEnv = append(newEnv, v)
	}
	ps.Containers[0].Env = newEnv
}
