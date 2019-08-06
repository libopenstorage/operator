package portworx

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/openstorage/api"
	"github.com/libopenstorage/openstorage/pkg/dbg"
	corev1alpha2 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha2"
	"github.com/libopenstorage/operator/pkg/mock"
	"github.com/portworx/kvdb"
	"github.com/portworx/kvdb/consul"
	e2 "github.com/portworx/kvdb/etcd/v2"
	e3 "github.com/portworx/kvdb/etcd/v3"
	"github.com/portworx/kvdb/mem"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestString(t *testing.T) {
	driver := portworx{}
	require.Equal(t, driverName, driver.String())
}

func TestInit(t *testing.T) {
	driver := portworx{}

	// Nil k8s client
	recorder := record.NewFakeRecorder(0)
	err := driver.Init(nil, recorder)
	require.EqualError(t, err, "kubernetes client cannot be nil")

	// Nil k8s event recorder
	k8sClient := fake.NewFakeClient()
	err = driver.Init(k8sClient, nil)
	require.EqualError(t, err, "event recorder cannot be nil")

	// Valid k8s client
	err = driver.Init(k8sClient, recorder)
	require.NoError(t, err)
	require.Equal(t, k8sClient, driver.k8sClient)
}

func TestGetSelectorLabels(t *testing.T) {
	driver := portworx{}
	expectedLabels := map[string]string{labelKeyName: driverName}
	require.Equal(t, expectedLabels, driver.GetSelectorLabels())
}

func TestGetStorkDriverName(t *testing.T) {
	driver := portworx{}
	actualStorkDriverName, err := driver.GetStorkDriverName()
	require.NoError(t, err)
	require.Equal(t, storkDriverName, actualStorkDriverName)
}

func TestGetStorkEnvList(t *testing.T) {
	driver := portworx{}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	envVars := driver.GetStorkEnvList(cluster)

	require.Len(t, envVars, 1)
	require.Equal(t, envKeyPortworxNamespace, envVars[0].Name)
	require.Equal(t, cluster.Namespace, envVars[0].Value)
}

func TestSetDefaultsOnStorageCluster(t *testing.T) {
	k8s.Instance().SetClient(fakek8sclient.NewSimpleClientset(), nil, nil, nil, nil, nil)
	driver := portworx{}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedPlacement := &corev1alpha2.PlacementSpec{
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
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)

	// Empty kvdb spec should still set internal kvdb as default
	cluster.Spec.Kvdb = &corev1alpha2.KvdbSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)

	// Should not overwrite complete kvdb spec if endpoints are empty
	cluster.Spec.Kvdb = &corev1alpha2.KvdbSpec{
		AuthSecret: "test-secret",
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, "test-secret", cluster.Spec.Kvdb.AuthSecret)

	// If endpoints are set don't set internal kvdb
	cluster.Spec.Kvdb = &corev1alpha2.KvdbSpec{
		Endpoints: []string{"endpoint1"},
	}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.False(t, cluster.Spec.Kvdb.Internal)

	// Don't overwrite secrets provider if already set
	cluster.Spec.SecretsProvider = stringPtr("aws-kms")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "aws-kms", *cluster.Spec.SecretsProvider)

	// Don't overwrite secrets provider if set to empty
	cluster.Spec.SecretsProvider = stringPtr("")
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, "", *cluster.Spec.SecretsProvider)

	// Don't overwrite start port if already set
	startPort := uint32(10001)
	cluster.Spec.StartPort = &startPort
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, uint32(10001), *cluster.Spec.StartPort)

	// Add default placement if node placement is nil
	cluster.Spec.Placement = &corev1alpha2.PlacementSpec{}
	driver.SetDefaultsOnStorageCluster(cluster)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestSetDefaultsOnStorageClusterForOpenshift(t *testing.T) {
	k8s.Instance().SetClient(fakek8sclient.NewSimpleClientset(), nil, nil, nil, nil, nil)
	driver := portworx{}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsOpenshift: "true",
			},
		},
	}

	expectedPlacement := &corev1alpha2.PlacementSpec{
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
	}

	driver.SetDefaultsOnStorageCluster(cluster)

	require.True(t, cluster.Spec.Kvdb.Internal)
	require.Equal(t, defaultSecretsProvider, *cluster.Spec.SecretsProvider)
	require.Equal(t, uint32(defaultStartPort), *cluster.Spec.StartPort)
	require.Equal(t, expectedPlacement, cluster.Spec.Placement)
}

func TestUpdateClusterStatus(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Status None
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Id:   "cluster-id",
			Name: "cluster-name",
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "cluster-name", cluster.Status.ClusterName)
	require.Equal(t, "cluster-id", cluster.Status.ClusterUID)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Init
	expectedClusterResp.Cluster.Status = api.Status_STATUS_INIT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Offline
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OFFLINE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Error
	expectedClusterResp.Cluster.Status = api.Status_STATUS_ERROR
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Offline", cluster.Status.Phase)

	// Status Decommission
	expectedClusterResp.Cluster.Status = api.Status_STATUS_DECOMMISSION
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)

	// Status Maintenance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_MAINTENANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NeedsReboot
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NEEDS_REBOOT
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status NotInQuorum
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status NotInQuorumNoStorage
	expectedClusterResp.Cluster.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "NotInQuorum", cluster.Status.Phase)

	// Status Ok
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDown
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DOWN
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDegraded
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageRebalance
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status StorageDriveReplace
	expectedClusterResp.Cluster.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Online", cluster.Status.Phase)

	// Status Invalid
	expectedClusterResp.Cluster.Status = api.Status(9999)
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)
	require.Equal(t, "Unknown", cluster.Status.Phase)
}

func TestUpdateClusterStatusForNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// Mock cluster inspect response
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Mock node enumerate response
	expectedNodeOne := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "10.0.1.1",
		MgmtIp:            "10.0.1.2",
		Status:            api.Status_STATUS_NONE,
	}
	expectedNodeTwo := &api.StorageNode{
		Id:                "node-2",
		SchedulerNodeName: "node-two",
		DataIp:            "10.0.2.1",
		MgmtIp:            "10.0.2.2",
		Status:            api.Status_STATUS_OK,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne, expectedNodeTwo},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	// Status None
	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	nodeStatus := &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-1", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.1.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.1.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, corev1alpha2.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha2.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-two", cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, nodeStatus.OwnerReferences, 1)
	require.Equal(t, cluster.Name, nodeStatus.OwnerReferences[0].Name)
	require.Equal(t, driver.GetSelectorLabels(), nodeStatus.Labels)
	require.Equal(t, "node-2", nodeStatus.Status.NodeUID)
	require.Equal(t, "10.0.2.1", nodeStatus.Status.Network.DataIP)
	require.Equal(t, "10.0.2.2", nodeStatus.Status.Network.MgmtIP)
	require.Len(t, nodeStatus.Status.Conditions, 1)
	require.Equal(t, corev1alpha2.NodeState, nodeStatus.Status.Conditions[0].Type)
	require.Equal(t, corev1alpha2.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Return only one node in enumerate for future tests
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNodeOne},
	}

	// Status Init
	expectedNodeOne.Status = api.Status_STATUS_INIT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeInit, nodeStatus.Status.Conditions[0].Status)

	// Status Offline
	expectedNodeOne.Status = api.Status_STATUS_OFFLINE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Error
	expectedNodeOne.Status = api.Status_STATUS_ERROR
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorum
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeNotInQuorum, nodeStatus.Status.Conditions[0].Status)

	// Status NotInQuorumNoStorage
	expectedNodeOne.Status = api.Status_STATUS_NOT_IN_QUORUM_NO_STORAGE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeNotInQuorum, nodeStatus.Status.Conditions[0].Status)

	// Status NeedsReboot
	expectedNodeOne.Status = api.Status_STATUS_NEEDS_REBOOT
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeOffline, nodeStatus.Status.Conditions[0].Status)

	// Status Decommission
	expectedNodeOne.Status = api.Status_STATUS_DECOMMISSION
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeDecommissioned, nodeStatus.Status.Conditions[0].Status)

	// Status Maintenance
	expectedNodeOne.Status = api.Status_STATUS_MAINTENANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeMaintenance, nodeStatus.Status.Conditions[0].Status)

	// Status Ok
	expectedNodeOne.Status = api.Status_STATUS_OK
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDown
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DOWN
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeOnline, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDegraded
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DEGRADED
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageRebalance
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_REBALANCE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status StorageDriveReplace
	expectedNodeOne.Status = api.Status_STATUS_STORAGE_DRIVE_REPLACE
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeDegraded, nodeStatus.Status.Conditions[0].Status)

	// Status Invalid
	expectedNodeOne.Status = api.Status(9999)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatus = &corev1alpha2.StorageNodeStatus{}
	err = get(k8sClient, nodeStatus, "node-one", cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, corev1alpha2.NodeUnknown, nodeStatus.Status.Conditions[0].Status)
}

func TestUpdateClusterStatusWithoutPortworxService(t *testing.T) {
	// Fake client without service
	k8sClient := fakeK8sClient()

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUpdateClusterStatusServiceWithoutClusterIP(t *testing.T) {
	// Fake client with a service that does not have cluster ip
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get endpoint")
}

func TestUpdateClusterStatusServiceGrpcServerError(t *testing.T) {
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "127.0.0.1",
		},
	})

	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error connecting to GRPC server")
}

func TestUpdateClusterStatusInspectClusterFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	// Error from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, fmt.Errorf("InspectCurrent error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "InspectCurrent error")

	// Nil response from InspectCurrent API
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to inspect cluster")

	// Nil cluster object in the response of InspectCurrent API
	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: nil,
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty ClusterInspect response")
}

func TestUpdateClusterStatusEnumerateNodesFailure(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	// Error from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, fmt.Errorf("node Enumerate error")).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "node Enumerate error")

	// Nil response from node Enumerate API
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(nil, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to enumerate nodes")

	// Empty list of nodes should not create any StorageNodeStatus objects
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Nil list of nodes should not create any StorageNodeStatus objects
	expectedNodeEnumerateResp.Nodes = nil
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)
}

func TestUpdateClusterStatusShouldUpdateStatusIfChanged(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_ERROR,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)

	expectedNode := &api.StorageNode{
		Id:                "node-1",
		SchedulerNodeName: "node-one",
		DataIp:            "1.1.1.1",
		Status:            api.Status_STATUS_MAINTENANCE,
	}
	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{expectedNode},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Offline", cluster.Status.Phase)
	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha2.NodeMaintenance, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "1.1.1.1", nodeStatusList.Items[0].Status.Network.DataIP)

	// Update status based on the latest object
	expectedClusterResp.Cluster.Status = api.Status_STATUS_OK
	expectedNode.Status = api.Status_STATUS_OK
	expectedNode.DataIp = "2.2.2.2"
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		Times(1)
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	require.Equal(t, "Online", cluster.Status.Phase)
	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-1", nodeStatusList.Items[0].Status.NodeUID)
	require.Equal(t, corev1alpha2.NodeOnline, nodeStatusList.Items[0].Status.Conditions[0].Status)
	require.Equal(t, "2.2.2.2", nodeStatusList.Items[0].Status.Network.DataIP)
}

func TestUpdateClusterStatusWithoutSchedulerNodeName(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:       "node-uid",
				DataIp:   "1.1.1.1",
				MgmtIp:   "2.2.2.2",
				Hostname: "node-hostname",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		AnyTimes()

	// Fake a node object without matching ip address or hostname to storage node
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Empty(t, nodeStatusList.Items)

	// Fake a node object with matching data ip address
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeInternalIP,
						Address: "1.1.1.1",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching mgmt ip address
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeExternalIP,
						Address: "2.2.2.2",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeHostName,
						Address: "node-hostname",
					},
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)

	// Fake a node object with matching hostname from labels
	deleteObj(k8sClient, &nodeStatusList.Items[0])
	k8s.Instance().SetClient(
		fakek8sclient.NewSimpleClientset(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-name",
				Labels: map[string]string{
					"kubernetes.io/hostname": "node-hostname",
				},
			},
		}),
		nil, nil, nil, nil, nil,
	)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-uid", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStatusForNonExistingNodes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Node got removed from storage driver
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestUpdateClusterStatusShouldDeleteStatusIfSchedulerNodeNameNotPresent(t *testing.T) {
	// Create fake k8s client without any nodes to lookup
	k8s.Instance().SetClient(fakek8sclient.NewSimpleClientset(), nil, nil, nil, nil, nil)

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Create the mock servers that can be used to mock SDK calls
	mockClusterServer := mock.NewMockOpenStorageClusterServer(mockCtrl)
	mockNodeServer := mock.NewMockOpenStorageNodeServer(mockCtrl)

	// Start a sdk server that implements the mock servers
	sdkServerIP := "127.0.0.1"
	sdkServerPort := 21883
	mockSdk := mock.NewSdkServer(mock.SdkServers{
		Cluster: mockClusterServer,
		Node:    mockNodeServer,
	})
	mockSdk.StartOnAddress(sdkServerIP, strconv.Itoa(sdkServerPort))
	defer mockSdk.Stop()

	// Create fake k8s client with fake service that will point the client
	// to the mock sdk server address
	k8sClient := fakeK8sClient(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: "kube-test",
		},
		Spec: v1.ServiceSpec{
			ClusterIP: sdkServerIP,
			Ports: []v1.ServicePort{
				{
					Name: pxSDKPortName,
					Port: int32(sdkServerPort),
				},
			},
		},
	})

	// Create driver object with the fake k8s client
	driver := portworx{
		k8sClient: k8sClient,
		recorder:  record.NewFakeRecorder(10),
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	expectedClusterResp := &api.SdkClusterInspectCurrentResponse{
		Cluster: &api.StorageCluster{
			Status: api.Status_STATUS_OK,
		},
	}
	mockClusterServer.EXPECT().
		InspectCurrent(gomock.Any(), &api.SdkClusterInspectCurrentRequest{}).
		Return(expectedClusterResp, nil).
		AnyTimes()

	expectedNodeEnumerateResp := &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-1",
				SchedulerNodeName: "node-one",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err := driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList := &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 2)

	// Scheduler node name missing for storage node
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id: "node-1",
			},
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)

	// Deleting already deleted StorageNodeStatus should not throw error
	expectedNodeEnumerateResp = &api.SdkNodeEnumerateWithFiltersResponse{
		Nodes: []*api.StorageNode{
			{
				Id:                "node-2",
				SchedulerNodeName: "node-two",
			},
		},
	}
	mockNodeServer.EXPECT().
		EnumerateWithFilters(gomock.Any(), &api.SdkNodeEnumerateWithFiltersRequest{}).
		Return(expectedNodeEnumerateResp, nil).
		Times(1)

	err = driver.UpdateStorageClusterStatus(cluster)
	require.NoError(t, err)

	nodeStatusList = &corev1alpha2.StorageNodeStatusList{}
	err = list(k8sClient, nodeStatusList)
	require.NoError(t, err)
	require.Len(t, nodeStatusList.Items, 1)
	require.Equal(t, "node-2", nodeStatusList.Items[0].Status.NodeUID)
}

func TestDeleteClusterWithoutDeleteStrategy(t *testing.T) {
	driver := portworx{
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiStatefulSetCreated:             true,
	}

	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// If no delete strategy is provided, condition should be complete
	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Equal(t, storageClusterDeleteMsg, condition.Reason)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiStatefulSetCreated)
}

func TestDeleteClusterWithUninstallStrategy(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiStatefulSetCreated:             true,
	}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiStatefulSetCreated)

	// Check condition
	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := getExpectedDaemonSet(t, "nodeWiper.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithCustomRepoRegistry(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRepo+"/px-node-wiper:2.1.2-rc1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomRegistry(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRegistry := "test-registry:1111"
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/portworx/px-node-wiper:2.1.2-rc1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithCustomNodeWiperImage(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	customRegistry := "test-registry:1111"
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
			CommonConfig: corev1alpha2.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  envKeyNodeWiperImage,
						Value: "test/node-wiper:v1",
					},
				},
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, customRegistry+"/test/node-wiper:v1",
		wiperDS.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestDeleteClusterWithUninstallStrategyForPKS(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiStatefulSetCreated:             true,
	}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationIsPKS: "true",
			},
		},
		Spec: corev1alpha2.StorageClusterSpec{
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiStatefulSetCreated)

	// Check condition
	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := getExpectedDaemonSet(t, "nodeWiperPKS.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithUninstallAndWipeStrategy(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient:                         k8sClient,
		pxAPIDaemonSetCreated:             true,
		volumePlacementStrategyCRDCreated: true,
		pvcControllerDeploymentCreated:    true,
		lhDeploymentCreated:               true,
		csiStatefulSetCreated:             true,
	}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// All components should be marked as not created
	require.False(t, driver.pxAPIDaemonSetCreated)
	require.False(t, driver.volumePlacementStrategyCRDCreated)
	require.False(t, driver.pvcControllerDeploymentCreated)
	require.False(t, driver.lhDeploymentCreated)
	require.False(t, driver.csiStatefulSetCreated)

	// Check condition
	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Equal(t, "Started node wiper daemonset", condition.Reason)

	// Check wiper daemonset
	expectedDaemonSet := getExpectedDaemonSet(t, "nodeWiperWithWipe.yaml")
	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.Len(t, wiperDS.OwnerReferences, 1)
	require.Equal(t, cluster.Name, wiperDS.OwnerReferences[0].Name)
	require.Equal(t, expectedDaemonSet.Spec, wiperDS.Spec)
}

func TestDeleteClusterWithNodeAffinity(t *testing.T) {
	k8sClient := fake.NewFakeClient()
	driver := portworx{
		k8sClient: k8sClient,
	}
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			Placement: &corev1alpha2.PlacementSpec{
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
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	_, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check wiper daemonset
	wiperDS := &appsv1.DaemonSet{}
	err = get(k8sClient, wiperDS, pxNodeWiperDaemonSetName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, pxNodeWiperDaemonSetName, wiperDS.Name)
	require.Equal(t, cluster.Namespace, wiperDS.Namespace)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity)
	require.NotNil(t, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
	require.Equal(t, cluster.Spec.Placement.NodeAffinity, wiperDS.Spec.Template.Spec.Affinity.NodeAffinity)
}

func TestDeleteClusterWithUninstallWhenNodeWiperCreated(t *testing.T) {
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
	}
	k8sClient := fake.NewFakeClient(wiperDS)
	driver := portworx{
		k8sClient: k8sClient,
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallMsg)
}

func TestDeleteClusterWithUninstallWipeStrategyWhenNodeWiperCreated(t *testing.T) {
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			Kvdb: &corev1alpha2.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	// Check when daemon set's status is not even updated
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
	}
	k8sClient := fake.NewFakeClient(wiperDS)
	driver := portworx{
		k8sClient: k8sClient,
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [0] Total [0]")

	// Check when daemon set's status is updated
	wiperDS.Status.DesiredNumberScheduled = int32(2)
	err = k8sClient.Status().Update(context.TODO(), wiperDS)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [0] In Progress [2] Total [2]")

	// Check when only few pods are ready
	wiperPod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-1",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod1)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationInProgress, condition.Status)
	require.Contains(t, condition.Reason,
		"Wipe operation still in progress: Completed [1] In Progress [1] Total [2]")

	// Check when all pods are ready
	wiperPod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "wiper-2",
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
	err = k8sClient.Create(context.TODO(), wiperPod2)
	require.NoError(t, err)

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveConfigMaps(t *testing.T) {
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			Kvdb: &corev1alpha2.KvdbSpec{
				Internal: true,
			},
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: cluster.Namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	etcdConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      internalEtcdConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	cloudDriveConfigMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cloudDriveConfigMapPrefix + "pxcluster",
			Namespace: bootstrapCloudDriveNamespace,
		},
	}
	k8sClient := fake.NewFakeClient(wiperDS, wiperPod, etcdConfigMap, cloudDriveConfigMap)
	driver := portworx{
		k8sClient: k8sClient,
	}

	configMaps := &v1.ConfigMapList{}
	err := list(k8sClient, configMaps)
	require.NoError(t, err)
	require.Len(t, configMaps.Items, 2)

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	// Check condition
	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	// Check config maps are deleted
	configMaps = &v1.ConfigMapList{}
	err = list(k8sClient, configMaps)
	require.NoError(t, err)
	require.Empty(t, configMaps.Items)
}

func TestDeleteClusterWithUninstallWipeStrategyShouldRemoveKvdbData(t *testing.T) {
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			Kvdb: &corev1alpha2.KvdbSpec{
				Endpoints: []string{
					"etcd://kvdb1.com:2001",
					"etcd://kvdb2.com:2001",
				},
			},
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Test etcd v3 without http/https
	kvdbMem, err := kvdb.New(mem.Name, pxKvdbPrefix, nil, nil, dbg.Panicf)
	require.NoError(t, err)
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err := kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit http
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:http://kvdb1.com:2001",
		"etcd:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd v3 with explicit https
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e3.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test etcd base version
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdBaseVersion, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"etcd:https://kvdb1.com:2001",
		"etcd:https://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, e2.Name, name)
		require.ElementsMatch(t, []string{"https://kvdb1.com:2001", "https://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)

	// Test consul
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.ConsulVersion1, nil
	}
	cluster.Spec.Kvdb.Endpoints = []string{
		"consul:http://kvdb1.com:2001",
		"consul:http://kvdb2.com:2001",
	}
	kvdbMem.Put(cluster.Name+"/foo", "bar", 0)
	newKVDB = func(name, _ string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		require.Equal(t, consul.Name, name)
		require.ElementsMatch(t, []string{"http://kvdb1.com:2001", "http://kvdb2.com:2001"}, machines)
		require.Empty(t, opts)
		return kvdbMem, nil
	}

	kp, err = kvdbMem.Get(cluster.Name + "/foo")
	require.NoError(t, err)
	require.Equal(t, "bar", string(kp.Value))

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationCompleted, condition.Status)
	require.Contains(t, condition.Reason, storageClusterUninstallAndWipeMsg)

	_, err = kvdbMem.Get(cluster.Name + "/foo")
	require.Error(t, err)
	require.Equal(t, kvdb.ErrNotFound, err)
}

func TestDeleteClusterWithUninstallWipeStrategyFailedRemoveKvdbData(t *testing.T) {
	cluster := &corev1alpha2.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha2.StorageClusterSpec{
			Kvdb: &corev1alpha2.KvdbSpec{
				Endpoints: []string{},
			},
			DeleteStrategy: &corev1alpha2.StorageClusterDeleteStrategy{
				Type: corev1alpha2.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}

	k8sClient := fakeClientWithWiperPod(cluster.Namespace)
	driver := portworx{
		k8sClient: k8sClient,
	}

	// Fail if no kvdb endpoints given
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return kvdb.New(mem.Name, prefix, machines, opts, dbg.Panicf)
	}

	condition, err := driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb type given in url
	cluster.Spec.Kvdb.Endpoints = []string{"zookeeper://kvdb.com:2001"}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if unknown kvdb version found
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "zookeeper1", nil
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")

	// Fail if error getting kvdb version
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return "", fmt.Errorf("kvdb version error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb version error")

	// Fail if error initializing kvdb
	cluster.Spec.Kvdb.Endpoints = []string{"etcd://kvdb.com:2001"}
	getKVDBVersion = func(_ string, url string, opts map[string]string) (string, error) {
		return kvdb.EtcdVersion3, nil
	}
	newKVDB = func(_, prefix string, machines []string, opts map[string]string, _ kvdb.FatalErrorCB) (kvdb.Kvdb, error) {
		return nil, fmt.Errorf("kvdb initialize error")
	}

	condition, err = driver.DeleteStorage(cluster)
	require.NoError(t, err)

	require.Equal(t, corev1alpha2.ClusterConditionTypeDelete, condition.Type)
	require.Equal(t, corev1alpha2.ClusterOperationFailed, condition.Status)
	require.Contains(t, condition.Reason, "Failed to wipe metadata")
	require.Contains(t, condition.Reason, "kvdb initialize error")
}

func fakeClientWithWiperPod(namespace string) client.Client {
	wiperDS := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: namespace,
			UID:       types.UID("wiper-ds-uid"),
		},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: 1,
		},
	}
	wiperPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{{UID: wiperDS.UID}},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{Ready: true}},
		},
	}
	return fake.NewFakeClient(wiperDS, wiperPod)
}
