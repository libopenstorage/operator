package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	schedcomp "k8s.io/component-base/config/v1alpha1"
	schedconfig "k8s.io/kube-scheduler/config/v1beta3"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

func TestStorkInstallation(t *testing.T) {
	testStorkInstallation(t, minSupportedK8sVersion)
	testStorkInstallation(t, policyDecoderChangeVersion)
	testStorkInstallation(t, "1.18.0")
	testStorkInstallation(t, "1.23.0")
	testStorkInstallation(t, "1.25.0")
}

func testStorkInstallation(t *testing.T, k8sVersionStr string) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Env: []v1.EnvVar{
					{
						Name:  "TEST",
						Value: "test-value",
					},
					{
						Name: "SECRET_ENV",
						ValueFrom: &v1.EnvVarSource{
							SecretKeyRef: &v1.SecretKeySelector{
								LocalObjectReference: v1.LocalObjectReference{
									Name: "secret-name",
								},
								Key: "secret-key",
							},
						},
					},
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(k8sVersionStr)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	k8sMinVersionForKubeSchedulerConfiguration, err := version.NewVersion(minK8sVersionForKubeSchedulerConfiguration)
	require.NoError(t, err)

	if k8sVersion.GreaterThanOrEqual(k8sMinVersionForKubeSchedulerConfiguration) {
		// Stork ConfigMap
		leaderElect := true
		schedulerName := storkDeploymentName
		expectedKubeSchedulerConfiguration := schedconfig.KubeSchedulerConfiguration{
			TypeMeta: metav1.TypeMeta{
				Kind:       "KubeSchedulerConfiguration",
				APIVersion: "kubescheduler.config.k8s.io/v1beta3",
			},
			LeaderElection: schedcomp.LeaderElectionConfiguration{
				LeaderElect:       &leaderElect,
				ResourceNamespace: "kube-test",
				ResourceName:      storkSchedDeploymentName,
				LeaseDuration:     metav1.Duration{Duration: 15 * time.Second},
				RenewDeadline:     metav1.Duration{Duration: 10 * time.Second},
				RetryPeriod:       metav1.Duration{Duration: 2 * time.Second},
				ResourceLock:      "leases",
			},
			Profiles: []schedconfig.KubeSchedulerProfile{
				{
					SchedulerName: &schedulerName,
				},
			},
			Extenders: []schedconfig.Extender{
				{
					URLPrefix: fmt.Sprintf(
						"http://%s.%s:%d",
						storkServiceName, "kube-test", storkServicePort,
					),
					FilterVerb:       "filter",
					PrioritizeVerb:   "prioritize",
					Weight:           5,
					EnableHTTPS:      false,
					NodeCacheCapable: false,
					HTTPTimeout:      metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		}

		var actualKubeSchedulerConfiguration = schedconfig.KubeSchedulerConfiguration{}
		storkConfigMap := &v1.ConfigMap{}
		err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
		require.NoError(t, err)
		require.Equal(t, storkConfigMapName, storkConfigMap.Name)
		require.Equal(t, cluster.Namespace, storkConfigMap.Namespace)
		require.Len(t, storkConfigMap.OwnerReferences, 1)
		require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)

		err = yaml.Unmarshal([]byte(storkConfigMap.Data["stork-config.yaml"]), &actualKubeSchedulerConfiguration)
		require.NoError(t, err)

		require.True(t, reflect.DeepEqual(expectedKubeSchedulerConfiguration.TypeMeta, actualKubeSchedulerConfiguration.TypeMeta))
		require.Equal(t, expectedKubeSchedulerConfiguration.Profiles[0].SchedulerName, actualKubeSchedulerConfiguration.Profiles[0].SchedulerName)
		require.True(t, reflect.DeepEqual(expectedKubeSchedulerConfiguration.LeaderElection, actualKubeSchedulerConfiguration.LeaderElection))
		require.True(t, reflect.DeepEqual(expectedKubeSchedulerConfiguration.Extenders[0], actualKubeSchedulerConfiguration.Extenders[0]))
	} else {
		// Stork ConfigMap
		expectedPolicy := SchedulerPolicy{
			Kind:       "Policy",
			APIVersion: "kubescheduler.config.k8s.io/v1",
			Extenders: []SchedulerExtender{
				{
					URLPrefix:      "http://stork-service.kube-test:8099",
					FilterVerb:     "filter",
					PrioritizeVerb: "prioritize",
					Weight:         5,
					HTTPTimeout:    metav1.Duration{Duration: 5 * time.Minute}.Nanoseconds(),
				},
			},
		}
		var actualPolicy SchedulerPolicy
		storkConfigMap := &v1.ConfigMap{}
		err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
		require.NoError(t, err)
		require.Equal(t, storkConfigMapName, storkConfigMap.Name)
		require.Equal(t, cluster.Namespace, storkConfigMap.Namespace)
		require.Len(t, storkConfigMap.OwnerReferences, 1)
		require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)

		err = json.Unmarshal([]byte(storkConfigMap.Data["policy.cfg"]), &actualPolicy)
		require.NoError(t, err)

		// Surprisingly the decoded object does not have kind and API version, both fields are empty. The encoded string
		// does have both. There is no functionality issue so far however we need to keep an eye on it.
		// The decoder and encoder are both following official k8s code and test code.
		// https://github.com/kubernetes/kubernetes/blob/release-1.21/pkg/scheduler/scheduler.go#L306
		// https://github.com/kubernetes/kubernetes/blob/release-1.21/test/integration/util/util.go#L432
		//
		// As a result of above, we only verify the extender instead of entire policy object here.
		require.True(t, reflect.DeepEqual(expectedPolicy.Extenders[0], actualPolicy.Extenders[0]))
	}

	// ServiceAccounts
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 2)

	// Stork ServiceAccount
	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, storkServiceAccountName, storkSA.Name)
	require.Equal(t, cluster.Namespace, storkSA.Namespace)
	require.Len(t, storkSA.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkSA.OwnerReferences[0].Name)

	// Stork Scheduler ServiceAccount
	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, storkSchedServiceAccountName, storkSchedSA.Name)
	require.Equal(t, cluster.Namespace, storkSchedSA.Namespace)
	require.Len(t, storkSchedSA.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkSchedSA.OwnerReferences[0].Name)

	// ClusterRoles
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 2)

	// Stork ClusterRole
	expectedStorkCR := testutil.GetExpectedClusterRole(t, "storkClusterRole.yaml")
	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedStorkCR.Name, storkCR.Name)
	require.Empty(t, storkCR.OwnerReferences)
	require.ElementsMatch(t, expectedStorkCR.Rules, storkCR.Rules)

	// Stork Scheduler ClusterRole
	expectedSchedCR := testutil.GetExpectedClusterRole(t, "storkSchedClusterRole.yaml")
	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedSchedCR.Name, schedCR.Name)
	require.Empty(t, schedCR.OwnerReferences)
	require.ElementsMatch(t, expectedSchedCR.Rules, schedCR.Rules)

	// ClusterRoleBindings
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 2)

	// Stork ClusterRoleBinding
	expectedStorkCRB := testutil.GetExpectedClusterRoleBinding(t, "storkClusterRoleBinding.yaml")
	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedStorkCRB.Name, storkCRB.Name)
	require.Empty(t, storkCRB.OwnerReferences)
	require.ElementsMatch(t, expectedStorkCRB.Subjects, storkCRB.Subjects)
	require.Equal(t, expectedStorkCRB.RoleRef, storkCRB.RoleRef)

	// Stork Scheduler ClusterRoleBinding
	expectedSchedCRB := testutil.GetExpectedClusterRoleBinding(t, "storkSchedClusterRoleBinding.yaml")
	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedSchedCRB.Name, schedCRB.Name)
	require.Empty(t, schedCRB.OwnerReferences)
	require.ElementsMatch(t, expectedSchedCRB.Subjects, schedCRB.Subjects)
	require.Equal(t, expectedSchedCRB.RoleRef, schedCRB.RoleRef)

	// Stork Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 1)

	expectedService := testutil.GetExpectedService(t, "storkService.yaml")
	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedService.Name, storkService.Name)
	require.Equal(t, expectedService.Namespace, storkService.Namespace)
	require.Len(t, storkService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkService.OwnerReferences[0].Name)
	require.Equal(t, expectedService.Labels, storkService.Labels)
	require.Equal(t, expectedService.Spec, storkService.Spec)

	// Deployments
	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Len(t, deploymentList.Items, 2)

	// Stork Deployment
	expectedStorkDeployment := testutil.GetExpectedDeployment(t, "storkDeployment.yaml")
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedStorkDeployment.Name, storkDeployment.Name)
	require.Equal(t, expectedStorkDeployment.Namespace, storkDeployment.Namespace)
	require.Len(t, storkDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedStorkDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	storkDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedStorkDeployment.Labels, storkDeployment.Labels)
	require.Equal(t, expectedStorkDeployment.Annotations, storkDeployment.Annotations)
	require.Equal(t, expectedStorkDeployment.Spec, storkDeployment.Spec)

	// Sched Scheduler Deployment
	if k8sVersion.GreaterThanOrEqual(k8sMinVersionForKubeSchedulerConfiguration) {
		expectedSchedDeployment := testutil.GetExpectedDeployment(t, "storkSchedKubeSchedConfigDeployment.yaml")
		schedDeployment := &appsv1.Deployment{}
		err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
		require.NoError(t, err)
		require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
		require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
		require.Len(t, schedDeployment.OwnerReferences, 1)
		require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
		// Ignoring resource comparison as the parsing from string creates different objects
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
		schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
		require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = strings.Replace(
			expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image, minK8sVersionForKubeSchedulerConfiguration, k8sVersionStr, -1)
		require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)
	} else {
		expectedSchedDeployment := testutil.GetExpectedDeployment(t, "storkSchedDeployment.yaml")
		schedDeployment := &appsv1.Deployment{}
		err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
		require.NoError(t, err)
		require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
		require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
		require.Len(t, schedDeployment.OwnerReferences, 1)
		require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
		// Ignoring resource comparison as the parsing from string creates different objects
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
		schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
		require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
		minK8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
		if k8sVersion.GreaterThanOrEqual(minK8sVersion) {
			expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = "k8s.gcr.io/kube-scheduler-amd64:v1.21.0"
		}
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = strings.Replace(
			expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image, minSupportedK8sVersion, k8sVersionStr, -1)
		require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)
	}

	// Stork Snapshot StorageClass
	storkStorageClass := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkStorageClass, storkSnapshotStorageClassName, "")
	require.NoError(t, err)
	require.Equal(t, storkSnapshotStorageClassName, storkStorageClass.Name)
	require.Empty(t, storkStorageClass.OwnerReferences)
	require.Equal(t, "stork-snapshot", storkStorageClass.Provisioner)
	require.True(t, *storkStorageClass.AllowVolumeExpansion)
}

func TestStorkSchedulerK8SVersions(t *testing.T) {
	var (
		k8sVersionStr string
		cluster       *corev1.StorageCluster
		k8sClient     client.Client
	)
	setup := func() {
		mockCtrl := gomock.NewController(t)
		defer mockCtrl.Finish()

		cluster = &corev1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "px-cluster",
				Namespace: "kube-test",
			},
			Spec: corev1.StorageClusterSpec{
				Stork: &corev1.StorkSpec{
					Enabled: true,
					Image:   "osd/stork:test",
					Env: []v1.EnvVar{
						{
							Name:  "TEST",
							Value: "test-value",
						},
						{
							Name: "SECRET_ENV",
							ValueFrom: &v1.EnvVarSource{
								SecretKeyRef: &v1.SecretKeySelector{
									LocalObjectReference: v1.LocalObjectReference{
										Name: "secret-name",
									},
									Key: "secret-key",
								},
							},
						},
					},
				},
			},
		}

		coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
		k8sVersion, _ := version.NewVersion(k8sVersionStr)
		driver := testutil.MockDriver(mockCtrl)
		k8sClient = testutil.FakeK8sClient(cluster)
		controller := Controller{
			client:            k8sClient,
			Driver:            driver,
			kubernetesVersion: k8sVersion,
			nodeInfoMap:       make(map[string]*k8s.NodeInfo),
		}

		driverEnvs := map[string]*v1.EnvVar{
			"PX_NAMESPACE": {
				Name:  "PX_NAMESPACE",
				Value: cluster.Namespace,
			},
		}
		driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
		driver.EXPECT().GetStorkEnvMap(cluster).
			Return(driverEnvs).
			AnyTimes()

		err := controller.syncStork(cluster)
		require.NoError(t, err)
	}

	k8sVersionStr = "1.22.0"
	setup()

	expectedSchedDeployment := testutil.GetExpectedDeployment(t, "storkSchedVersionedDeployment.yaml")
	schedDeployment := &appsv1.Deployment{}
	err := testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)

	k8sVersionStr = "1.22.5"
	setup()

	expectedSchedDeployment = testutil.GetExpectedDeployment(t, "storkSchedVersionedDeployment.yaml")
	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)

	k8sVersionStr = "1.21.0"
	setup()

	expectedSchedDeployment = testutil.GetExpectedDeployment(t, "storkSchedVersionedDeployment.yaml")
	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = strings.Replace(
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image, "1.21.4", k8sVersionStr, -1)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)

	k8sVersionStr = "1.23.0"
	setup()

	expectedSchedDeployment = testutil.GetExpectedDeployment(t, "storkSchedKubeSchedConfigDeployment.yaml")
	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = strings.Replace(
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image, minK8sVersionForKubeSchedulerConfiguration, k8sVersionStr, -1)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)

	k8sVersionStr = "1.25.0"
	setup()

	expectedSchedDeployment = testutil.GetExpectedDeployment(t, "storkSchedKubeSchedConfigDeployment.yaml")
	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image = strings.Replace(
		expectedSchedDeployment.Spec.Template.Spec.Containers[0].Image, minK8sVersionForKubeSchedulerConfiguration, k8sVersionStr, -1)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)
}

func TestStorkWithoutImage(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            testutil.FakeK8sClient(cluster),
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		recorder:          recorder,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork. stork image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)

	cluster.Spec.Stork.Image = ""
	err = controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork. stork image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)
}

func TestStorkWithDesiredImage(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{
				Stork: "osd/stork:status",
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "docker.io/osd/stork:status", storkDeployment.Spec.Template.Spec.Containers[0].Image)

	// If image is present in spec, then use that instead of desired image
	cluster.Spec.Stork.Image = "osd/stork:spec"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "docker.io/osd/stork:spec", storkDeployment.Spec.Template.Spec.Containers[0].Image)
}

func TestStorkImageChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "docker.io/osd/stork:v1", storkDeployment.Spec.Template.Spec.Containers[0].Image)

	// Change the stork image
	cluster.Spec.Stork.Image = "osd/stork:v2"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "docker.io/osd/stork:v2", storkDeployment.Spec.Template.Spec.Containers[0].Image)
}

func TestStorkArgumentsChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// Check custom new arg is present
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.Containers[0].Command, 7)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--test-key=test-value",
	)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--verbose=true",
	)

	// Overwrite existing argument with new value
	cluster.Spec.Stork.Args["verbose"] = "false"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.Containers[0].Command, 7)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--verbose=false",
	)
}

func TestStorkEnvVarsChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Env: []v1.EnvVar{
					{
						Name:  "FOO",
						Value: "foo",
					},
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// Check envs are passed to deployment
	expectedEnvs := []v1.EnvVar{
		{
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
		{
			Name:  "STORK-NAMESPACE",
			Value: cluster.Namespace,
		},
		{
			Name:  "FOO",
			Value: "foo",
		},
	}
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)

	// Overwrite existing envs
	cluster.Spec.Stork.Env[0].Value = "bar"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedEnvs[2].Value = "bar"
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)

	// Add new env vars
	newEnv := v1.EnvVar{Name: "BAZ", Value: "baz"}
	cluster.Spec.Stork.Env = append(cluster.Spec.Stork.Env, newEnv)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedEnvs = append(expectedEnvs, newEnv)
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)
}

func TestStorkCustomRegistryChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	customRegistry := "test-registry:1111"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			ImagePullPolicy:     v1.PullIfNotPresent,
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Custom registry should be applied to the images
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: Updated custom registry should be applied to the images
	customRegistry = "test-registry:2222"
	cluster.Spec.CustomImageRegistry = customRegistry

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: If empty, remove custom registry from the images
	cluster.Spec.CustomImageRegistry = ""

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: Custom registry should be added back if not present in images
	customRegistry = "test-registry:3333"
	cluster.Spec.CustomImageRegistry = customRegistry
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkCustomRepoRegistryChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			ImagePullPolicy:     v1.PullIfNotPresent,
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Custom repo-registry should be applied to the images
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: Updated custom repo-registry should be applied to the images
	customRepo = "test-registry:1111/new-repo"
	cluster.Spec.CustomImageRegistry = customRepo

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: If empty, remove custom repo-registry from the images
	cluster.Spec.CustomImageRegistry = ""

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"docker.io/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Case: Custom repo-registry should be added back if not present in images
	customRepo = "test-registry:1111/newest-repo"
	cluster.Spec.CustomImageRegistry = customRepo
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-scheduler-amd64:v"+k8sVersion.String(),
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkImagePullSecretChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	imagePullSecret := "pull-secret"
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
			},
			ImagePullSecret: &imagePullSecret,
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Image pull secret should be applied to the deployment
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	storkSchedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: Updated image pull secet should be applied to the deployment
	imagePullSecret = "new-secret"
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	// Case: If empty, remove image pull secret from the deployment
	imagePullSecret = ""
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets)

	// Case: If nil, remove image pull secret from the deployment
	cluster.Spec.ImagePullSecret = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets)

	// Case: Image pull secret should be added back if not present in deployment
	imagePullSecret = "pull-secret"
	cluster.Spec.ImagePullSecret = &imagePullSecret
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t, imagePullSecret, storkSchedDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name)
}

func TestStorkTolerationsChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	tolerations := []v1.Toleration{
		{
			Key:      "foo",
			Value:    "bar",
			Operator: v1.TolerationOpEqual,
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
			},
			Placement: &corev1.PlacementSpec{
				Tolerations: tolerations,
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Tolerations should be applied to the deployment
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: Updated tolerations should be applied to the deployment
	tolerations[0].Value = "baz"
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: New tolerations should be applied to the deployment
	tolerations = append(tolerations, v1.Toleration{
		Key:      "must-exist",
		Operator: v1.TolerationOpExists,
		Effect:   v1.TaintEffectNoExecute,
	})
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: Removed tolerations should be removed from the deployment
	tolerations = []v1.Toleration{tolerations[0]}
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: If tolerations are empty, should be removed from the deployment
	cluster.Spec.Placement.Tolerations = []v1.Toleration{}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: Tolerations should be added back if not present in deployment
	cluster.Spec.Placement.Tolerations = tolerations
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, tolerations, storkSchedDeployment.Spec.Template.Spec.Tolerations)

	// Case: If placement is empty, deployment should not have tolerations
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Tolerations)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkSchedDeployment.Spec.Template.Spec.Tolerations)
}

func TestStorkNodeAffinityChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	nodeAffinity := &v1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
			NodeSelectorTerms: []v1.NodeSelectorTerm{
				{
					MatchExpressions: []v1.NodeSelectorRequirement{
						{
							Key:      "px/enabled",
							Operator: v1.NodeSelectorOpNotIn,
							Values:   []string{"false"},
						},
					},
				},
			},
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
			},
			Placement: &corev1.PlacementSpec{
				NodeAffinity: nodeAffinity,
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Node affinity should be applied to the deployment
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	storkSchedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkSchedDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: Updated node affinity should be applied to the deployment
	nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
		NodeSelectorTerms[0].
		MatchExpressions[0].
		Key = "px/disabled"
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkSchedDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If node affinity is removed, it should be removed from the deployment
	cluster.Spec.Placement.NodeAffinity = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkSchedDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: Node affinity should be added back if not present in deployment
	cluster.Spec.Placement.NodeAffinity = nodeAffinity
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, nodeAffinity, storkSchedDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	// Case: If placement is nil, node affinity should be removed from the deployment
	cluster.Spec.Placement = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Affinity.NodeAffinity)

	storkSchedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkSchedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkSchedDeployment.Spec.Template.Spec.Affinity.NodeAffinity)
}

func TestStorkVolumesChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	volumeSpecs := []corev1.VolumeSpec{
		{
			Name:      "testvol",
			MountPath: "/var/testvol",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/host/test",
				},
			},
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
				Volumes: volumeSpecs,
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Case: Volumes should be applied to the deployment
	volumes, volumeMounts := expectedVolumesAndMounts(volumeSpecs)

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, storkDeployment.Spec.Template.Spec.Volumes)
	require.ElementsMatch(t, volumeMounts,
		storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: Updated volumes should be applied to the deployment
	propagation := v1.MountPropagationBidirectional
	pathType := v1.HostPathDirectory
	volumeSpecs[0].MountPropagation = &propagation
	volumeSpecs[0].HostPath.Type = &pathType
	cluster.Spec.Stork.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, storkDeployment.Spec.Template.Spec.Volumes)
	require.ElementsMatch(t, volumeMounts,
		storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: New volumes should be applied to the deployment
	volumeSpecs = append(volumeSpecs, corev1.VolumeSpec{
		Name:      "testvol2",
		MountPath: "/var/testvol2",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName: "volume-secret",
			},
		},
	})
	cluster.Spec.Stork.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, storkDeployment.Spec.Template.Spec.Volumes)
	require.ElementsMatch(t, volumeMounts,
		storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: Removed volumes should be removed from the deployment
	volumeSpecs = []corev1.VolumeSpec{volumeSpecs[0]}
	cluster.Spec.Stork.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, storkDeployment.Spec.Template.Spec.Volumes)
	require.ElementsMatch(t, volumeMounts,
		storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: If volumes are empty, should be removed from the deployment
	cluster.Spec.Stork.Volumes = []corev1.VolumeSpec{}
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Volumes)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: Volumes should be added back if not present in deployment
	cluster.Spec.Stork.Volumes = volumeSpecs
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)
	volumes, volumeMounts = expectedVolumesAndMounts(volumeSpecs)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, volumes, storkDeployment.Spec.Template.Spec.Volumes)
	require.ElementsMatch(t, volumeMounts,
		storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)

	// Case: If volumes is nil, deployment should not have volumes
	cluster.Spec.Stork.Volumes = nil
	err = k8sClient.Update(context.TODO(), cluster)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Volumes)
	require.Nil(t, storkDeployment.Spec.Template.Spec.Containers[0].VolumeMounts)
}

func TestStorkAndStorkSchedulerResources(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity := resource.MustParse(defaultStorkCPU)
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Set custom resources
	cluster.Spec.Stork.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("4Gi"),
			v1.ResourceCPU:    resource.MustParse("400m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceCPU:    resource.MustParse("800m"),
		},
	}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, *cluster.Spec.Stork.Resources, deployment.Spec.Template.Spec.Containers[0].Resources)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, *cluster.Spec.Stork.Resources, deployment.Spec.Template.Spec.Containers[0].Resources)

	cluster.Spec.Stork.Resources = &v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("2Gi"),
			v1.ResourceCPU:    resource.MustParse("200m"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceMemory: resource.MustParse("6Gi"),
			v1.ResourceCPU:    resource.MustParse("600m"),
		},
	}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, *cluster.Spec.Stork.Resources, deployment.Spec.Template.Spec.Containers[0].Resources)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, *cluster.Spec.Stork.Resources, deployment.Spec.Template.Spec.Containers[0].Resources)
}

func TestStorkCPUChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity := resource.MustParse(defaultStorkCPU)
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for Stork deployment
	expectedCPU := "0.2"
	cluster.Annotations = map[string]string{annotationStorkCPU: expectedCPU}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestStorkSchedulerCPUChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity := resource.MustParse(defaultStorkCPU)
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for Stork scheduler deployment
	expectedCPU := "0.2"
	cluster.Annotations = map[string]string{annotationStorkSchedCPU: expectedCPU}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestStorkInvalidCPU(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationStorkCPU: "invalid-cpu",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()

	// Should not return error, instead raise an event
	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestStorkSchedulerInvalidCPU(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationStorkSchedCPU: "invalid-cpu",
			},
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// Should not return error, instead raise an event
	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestStorkSchedulerRollbackImageChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Change the image of stork scheduler deployment
	deployment.Spec.Template.Spec.Containers[0].Image = "foo/bar:v1"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkSchedulerImageWithNewerK8sVersion(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion("1.18.7")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).Return(nil).AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.18 release train
	k8sVersion, _ = version.NewVersion("1.18.6")
	controller.kubernetesVersion = k8sVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Newer patches in Kubernetes 1.17 release train
	k8sVersion, _ = version.NewVersion("1.17.10")
	controller.kubernetesVersion = k8sVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.17 release train
	k8sVersion, _ = version.NewVersion("1.17.9")
	controller.kubernetesVersion = k8sVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Newer patches in Kubernetes 1.16 release train
	k8sVersion, _ = version.NewVersion("1.16.14")
	controller.kubernetesVersion = k8sVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"k8s.gcr.io/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Older patches in Kubernetes 1.16 release train
	k8sVersion, _ = version.NewVersion("1.16.13")
	controller.kubernetesVersion = k8sVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-scheduler-amd64:v"+k8sVersion.String(),
		deployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkSchedulerRollbackCommandChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedCommand := deployment.Spec.Template.Spec.Containers[0].Command

	// Change the command of stork scheduler deployment
	deployment.Spec.Template.Spec.Containers[0].Command = append(
		deployment.Spec.Template.Spec.Containers[0].Command,
		"--new-arg=test",
	)
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedCommand, deployment.Spec.Template.Spec.Containers[0].Command)
}

func TestStorkInstallWithImagePullPolicy(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		v1.PullIfNotPresent,
		storkDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		v1.PullIfNotPresent,
		schedDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)
}

func TestStorkInstallWithHostNetwork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	hostNetwork := true
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			ImagePullPolicy: v1.PullIfNotPresent,
			Stork: &corev1.StorkSpec{
				Enabled:     true,
				Image:       "osd/stork:test",
				HostNetwork: &hostNetwork,
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// TestCase: Stork host network is set to true
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.True(t, storkDeployment.Spec.Template.Spec.HostNetwork)

	// TestCase: Stork host network is set to false
	hostNetwork = false
	cluster.Spec.Stork.HostNetwork = &hostNetwork

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.False(t, storkDeployment.Spec.Template.Spec.HostNetwork)

	// TestCase: Stork host network is nil
	hostNetwork = true
	cluster.Spec.Stork.HostNetwork = &hostNetwork
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.True(t, storkDeployment.Spec.Template.Spec.HostNetwork)

	cluster.Spec.Stork.HostNetwork = nil
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.False(t, storkDeployment.Spec.Template.Spec.HostNetwork)
}

func TestStorkWithConfigReconciliationDisabled(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// TestCase: Deploy default policy when stork is deployed
	defaultPolicy := &SchedulerPolicy{
		Kind:       "Policy",
		APIVersion: "kubescheduler.config.k8s.io/v1",
		Extenders: []SchedulerExtender{
			{
				URLPrefix:      "http://stork-service.kube-test:8099",
				FilterVerb:     "filter",
				PrioritizeVerb: "prioritize",
				Weight:         5,
				HTTPTimeout:    metav1.Duration{Duration: 5 * time.Minute}.Nanoseconds(),
			},
		},
	}
	defaultPolicyBytes, _ := json.Marshal(defaultPolicy)

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkConfigMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)
	require.Equal(t, string(defaultPolicyBytes), storkConfigMap.Data["policy.cfg"])

	// TestCase: Reconcile to original policy if changed by the user
	modifiedPolicy := *defaultPolicy
	modifiedPolicy.Extenders[0].PrioritizeVerb = ""
	modifiedPolicy.Extenders[0].Weight = 10
	modifiedPolicyBytes, _ := json.Marshal(modifiedPolicy)

	storkConfigMap.Data["policy.cfg"] = string(modifiedPolicyBytes)
	err = k8sClient.Update(context.TODO(), storkConfigMap)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkConfigMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)
	require.Equal(t, string(defaultPolicyBytes), storkConfigMap.Data["policy.cfg"])

	// TestCase: Do not reconcile to original policy if user has disabled reconciliation
	storkConfigMap.Annotations = map[string]string{
		constants.AnnotationReconcileObject: "false",
	}
	storkConfigMap.Data["policy.cfg"] = string(modifiedPolicyBytes)
	err = k8sClient.Update(context.TODO(), storkConfigMap)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkConfigMap = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)
	require.Equal(t, string(modifiedPolicyBytes), storkConfigMap.Data["policy.cfg"])
}
func TestStorkSchedulerWithMissingLabelsFromSelector(t *testing.T) {
	mockCtrl := gomock.NewController(t)

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	// TestCase: name label should be present in the deployment selector and pod metadata
	err := controller.syncStork(cluster)
	require.NoError(t, err)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, schedDeployment.Spec.Selector.MatchLabels, 3)
	require.Equal(t, storkSchedDeploymentName, schedDeployment.Spec.Selector.MatchLabels["name"])
	require.Len(t, schedDeployment.Spec.Template.Labels, 3)
	require.Equal(t, storkSchedDeploymentName, schedDeployment.Spec.Template.Labels["name"])

	// TestCase: Set selector to empty and check the resource version
	schedDeployment.Spec.Selector = nil
	err = testutil.Update(k8sClient, schedDeployment)
	require.NoError(t, err)
	prevResourceVersion := schedDeployment.ResourceVersion

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, prevResourceVersion, schedDeployment.ResourceVersion)

	// TestCase: Selector does not have the name label. A new deployment should
	// be created as selector is an immutable field.
	schedDeployment.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"tier":      "control-plane",
			"component": "scheduler",
		},
	}
	schedDeployment.Spec.Template.Labels = schedDeployment.Spec.Selector.MatchLabels
	err = testutil.Update(k8sClient, schedDeployment)
	require.NoError(t, err)
	prevResourceVersion = schedDeployment.ResourceVersion
	rv, _ := strconv.Atoi(prevResourceVersion)
	require.True(t, rv > 1)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.NotEqual(t, prevResourceVersion, schedDeployment.ResourceVersion)
	// Resource version is reset to 1, indicating a new deployment has been created
	require.Equal(t, "1", schedDeployment.ResourceVersion)
}

func TestDisableStork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.NoError(t, err)

	// Disable Stork
	cluster.Spec.Stork.Enabled = false
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService = &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestRemoveStork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.NoError(t, err)

	// Remove Stork config
	cluster.Spec.Stork = nil
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService = &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestStorkDriverNotImplemented(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driver.EXPECT().GetStorkDriverName().Return("", fmt.Errorf("not supported"))
	driver.EXPECT().String().Return("mock")

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestStorkAndSchedulerDeploymentWithPodTopologySpreadConstraints(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	fakeNode := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node0",
			Labels: map[string]string{
				"topology.kubernetes.io/region": "region0",
			},
		},
	}
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(fakeNode)))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster, fakeNode)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// stork deployment topology constraints
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedConstraints := []v1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/region",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": storkDeploymentName,
					"tier": "control-plane",
				},
			},
		},
	}
	require.Equal(t, expectedConstraints, deployment.Spec.Template.Spec.TopologySpreadConstraints)

	// stork scheduler deployment topology constraints
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedConstraints = []v1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/region",
			WhenUnsatisfiable: v1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"tier":      "control-plane",
					"component": "scheduler",
					"name":      storkSchedDeploymentName,
				},
			},
		},
	}
	require.Equal(t, expectedConstraints, deployment.Spec.Template.Spec.TopologySpreadConstraints)
}

func TestStorkAndSchedulerDeploymentWithoutPodTopologySpreadConstraints(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			Stork: &corev1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	k8sVersion, _ := version.NewVersion(minSupportedK8sVersion)
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		nodeInfoMap:       make(map[string]*k8s.NodeInfo),
	}

	driverEnvs := map[string]*v1.EnvVar{
		"PX_NAMESPACE": {
			Name:  "PX_NAMESPACE",
			Value: cluster.Namespace,
		},
	}
	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvMap(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// stork deployment topology constraints
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, deployment.Spec.Template.Spec.TopologySpreadConstraints)

	// stork scheduler deployment topology constraints
	deployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Empty(t, deployment.Spec.Template.Spec.TopologySpreadConstraints)
}

func expectedVolumesAndMounts(volumeSpecs []corev1.VolumeSpec) ([]v1.Volume, []v1.VolumeMount) {
	expectedVolumeSpecs := make([]corev1.VolumeSpec, len(volumeSpecs))
	for i, v := range volumeSpecs {
		vCopy := v.DeepCopy()
		vCopy.Name = "user-" + v.Name
		expectedVolumeSpecs[i] = *vCopy
	}
	return util.ExtractVolumesAndMounts(expectedVolumeSpecs)
}
