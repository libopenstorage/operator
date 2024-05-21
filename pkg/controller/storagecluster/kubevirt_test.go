package storagecluster

import (
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/mock/mockcore"
	"github.com/libopenstorage/operator/pkg/mock/mockkubevirtdy"
	kubevirt "github.com/portworx/sched-ops/k8s/kubevirt-dynamic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClusterHasVMPods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockCoreOps := mockcore.NewMockOps(mockCtrl)
	kvmgr := newKubevirtManagerForTesting(mockCoreOps, mockkubevirtdy.NewMockOps(mockCtrl))

	// Test case: no virt-launcher pods
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(nil, nil)
	hasPods, err := kvmgr.ClusterHasVMPods()
	require.NoError(t, err)
	require.False(t, hasPods)

	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{}, nil)
	hasPods, err = kvmgr.ClusterHasVMPods()
	require.NoError(t, err)
	require.False(t, hasPods)

	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{}}, nil)
	hasPods, err = kvmgr.ClusterHasVMPods()
	require.NoError(t, err)
	require.False(t, hasPods)

	// Test case: virt-launcher pods exist
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "virt-launcher-1",
			},
		},
	}}, nil)
	hasPods, err = kvmgr.ClusterHasVMPods()
	require.NoError(t, err)
	require.True(t, hasPods)

	// Test case: error listing pods
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(nil, assert.AnError)
	hasPods, err = kvmgr.ClusterHasVMPods()
	require.Error(t, err)
	require.False(t, hasPods)
}

func TestGetVMPodsToEvictByNode(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockCoreOps := mockcore.NewMockOps(mockCtrl)
	mockKubeVirtOps := mockkubevirtdy.NewMockOps(mockCtrl)
	kvmgr := newKubevirtManagerForTesting(mockCoreOps, mockKubeVirtOps)

	// Test case: no virt-launcher pods
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(nil, nil)
	pods, err := kvmgr.GetVMPodsToEvictByNode()
	require.NoError(t, err)
	require.Empty(t, pods)

	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{}, nil)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.NoError(t, err)
	require.Empty(t, pods)

	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{}}, nil)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.NoError(t, err)
	require.Empty(t, pods)

	// Test case: running or pending virt-launcher pod exists and VMI is live-migratable
	for _, phase := range []v1.PodPhase{v1.PodRunning, v1.PodPending, v1.PodUnknown} {
		virtLauncherPod, vmi := getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
		virtLauncherPod.Status.Phase = phase
		mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{*virtLauncherPod}}, nil)
		mockKubeVirtOps.EXPECT().GetVirtualMachineInstance(gomock.Any(), gomock.Any(), vmi.Name).Return(vmi, nil)
		pods, err = kvmgr.GetVMPodsToEvictByNode()
		require.NoError(t, err)
		require.NotEmpty(t, pods)
		require.Len(t, pods, 1)
		require.Len(t, pods[virtLauncherPod.Spec.NodeName], 1)
		require.Equal(t, "virt-launcher-1", pods["node1"][0].Name)
	}

	// Test case: completed or failed virt-launcher pod should be ignored
	for _, phase := range []v1.PodPhase{v1.PodSucceeded, v1.PodFailed} {
		virtLauncherPod, _ := getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
		virtLauncherPod.Status.Phase = phase
		mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{*virtLauncherPod}}, nil)
		pods, err = kvmgr.GetVMPodsToEvictByNode()
		require.NoError(t, err)
		require.Empty(t, pods)
	}

	// Test case: vmi is not live-migratable
	virtLauncherPod, vmi := getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	vmi.LiveMigratable = false
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{*virtLauncherPod}}, nil)
	mockKubeVirtOps.EXPECT().GetVirtualMachineInstance(gomock.Any(), gomock.Any(), vmi.Name).Return(vmi, nil)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.NoError(t, err)
	require.Empty(t, pods)

	// Test case: error listing pods
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(nil, assert.AnError)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.Error(t, err)
	require.Empty(t, pods)

	// Test case: no VMI ownerRef in virt-launcher pod; no error should be returned
	virtLauncherPod, _ = getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	virtLauncherPod.OwnerReferences = nil
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{*virtLauncherPod}}, nil)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.NoError(t, err)
	require.Empty(t, pods)

	// Test case: error getting VMI
	virtLauncherPod, vmi = getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	mockCoreOps.EXPECT().ListPods(gomock.Any()).Return(&v1.PodList{Items: []v1.Pod{*virtLauncherPod}}, nil)
	mockKubeVirtOps.EXPECT().GetVirtualMachineInstance(gomock.Any(), gomock.Any(), vmi.Name).Return(nil, assert.AnError)
	pods, err = kvmgr.GetVMPodsToEvictByNode()
	require.Error(t, err)
	require.Empty(t, pods)
}

func TestStartEvictingVMPods(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	mockCoreOps := mockcore.NewMockOps(mockCtrl)
	mockKubeVirtOps := mockkubevirtdy.NewMockOps(mockCtrl)
	kvmgr := newKubevirtManagerForTesting(mockCoreOps, mockKubeVirtOps)
	hash := "rev1"
	expectedAnnotations := map[string]string{
		constants.AnnotationVMIMigrationSourceNode:    "node1",
		constants.AnnotationControllerRevisionHashKey: hash,
	}
	expectedLabels := map[string]string{
		constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
	}
	// Test case: no migration exists
	virtLauncherPod, vmi := getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	mockKubeVirtOps.EXPECT().ListVirtualMachineInstanceMigrations(gomock.Any(), "", gomock.Any()).Return(nil, nil)
	mockKubeVirtOps.EXPECT().CreateVirtualMachineInstanceMigrationWithParams(gomock.Any(), "", vmi.Name, "", "",
		expectedAnnotations, expectedLabels).Return(nil, nil)
	kvmgr.StartEvictingVMPods([]v1.Pod{*virtLauncherPod}, hash, func(message string) {})

	// Test case: migration in progress for the same VMI
	virtLauncherPod, vmi = getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	migrInProgress := []*kubevirt.VirtualMachineInstanceMigration{
		{
			VMIName:   vmi.Name,
			Completed: false,
			Phase:     "Running",
		},
	}
	mockKubeVirtOps.EXPECT().ListVirtualMachineInstanceMigrations(gomock.Any(), "", gomock.Any()).Return(migrInProgress, nil)
	// No expectation for call to CreateMigration since no new migration should be created
	kvmgr.StartEvictingVMPods([]v1.Pod{*virtLauncherPod}, hash, func(message string) {})

	// Test case: failed migration for the same VMI with the same controller revision hash from the same sourceNode.
	// Should not create a new migration.
	virtLauncherPod, vmi = getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	migrFailed := []*kubevirt.VirtualMachineInstanceMigration{
		{
			VMIName:     vmi.Name,
			Completed:   true,
			Failed:      true,
			Phase:       "Failed",
			Annotations: expectedAnnotations,
			Labels:      expectedLabels,
			SourceNode:  virtLauncherPod.Spec.NodeName,
		},
	}
	mockKubeVirtOps.EXPECT().ListVirtualMachineInstanceMigrations(gomock.Any(), "", gomock.Any()).Return(migrFailed, nil)
	// No expectation for call to CreateMigration since no new migration should be created
	eventMsg := ""
	kvmgr.StartEvictingVMPods([]v1.Pod{*virtLauncherPod}, hash, func(message string) { eventMsg = message })
	require.Contains(t, eventMsg, "Stop or migrate the VM so that the update of the storage node can proceed")

	// Test case: Failed or in-progress migrations for a different VMI, different revision hash, different source node etc.
	// Should create a new migration.
	virtLauncherPod, vmi = getTestVirtLauncherPodAndVMI("virt-launcher-1", "node1")
	migrations := []*kubevirt.VirtualMachineInstanceMigration{
		// different VMI
		{
			VMIName:     "different-vmi",
			Completed:   true,
			Failed:      true,
			Phase:       "Failed",
			Annotations: expectedAnnotations,
			Labels:      expectedLabels,
			SourceNode:  virtLauncherPod.Spec.NodeName,
		},
		// different revision hash
		{
			VMIName:   vmi.Name,
			Completed: true,
			Failed:    true,
			Phase:     "Failed",
			Annotations: map[string]string{
				constants.AnnotationVMIMigrationSourceNode:    "node1",
				constants.AnnotationControllerRevisionHashKey: "different-hash",
			},
			Labels: map[string]string{
				constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValue,
			},
			SourceNode: virtLauncherPod.Spec.NodeName,
		},
		// different source node
		{
			VMIName:   vmi.Name,
			Completed: true,
			Failed:    true,
			Phase:     "Failed",
			Annotations: map[string]string{
				constants.AnnotationVMIMigrationSourceNode:    "diferent-node",
				constants.AnnotationControllerRevisionHashKey: hash,
			},
			Labels: expectedLabels,
		},
	}
	mockKubeVirtOps.EXPECT().ListVirtualMachineInstanceMigrations(gomock.Any(), "", gomock.Any()).Return(migrations, nil)
	mockKubeVirtOps.EXPECT().CreateVirtualMachineInstanceMigrationWithParams(gomock.Any(), "", vmi.Name, "", "",
		expectedAnnotations, expectedLabels).Return(nil, nil)
	kvmgr.StartEvictingVMPods([]v1.Pod{*virtLauncherPod}, hash, func(message string) {})
}

func getTestVirtLauncherPodAndVMI(podName, nodeName string) (*v1.Pod, *kubevirt.VirtualMachineInstance) {
	vmiName := podName + "-vmi"
	return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: podName,
				OwnerReferences: []metav1.OwnerReference{
					{
						Kind: "VirtualMachineInstance",
						Name: vmiName,
					},
				},
			},
			Spec: v1.PodSpec{
				NodeName: nodeName,
			},
			Status: v1.PodStatus{
				Phase: v1.PodRunning,
			},
		}, &kubevirt.VirtualMachineInstance{
			LiveMigratable: true,
			Name:           vmiName,
		}
}
