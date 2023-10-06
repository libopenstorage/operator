//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	pxcomp "github.com/libopenstorage/operator/drivers/storage/portworx/component"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	portworxv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/controller/portworxdiag"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type diagTestSpec struct {
	DiagToCreate *portworxv1.PortworxDiag

	// Below are auto-populated by test main
	Cluster      *corev1.StorageCluster
	StorageNodes []corev1.StorageNode
}

const (
	DiagTestNamespace = "kube-system"

	diagVolumeLabelKey = "diag-label"
	diagVolumeLabelVal = "true"

	diagNodeLabelKey = "diag-node"
	diagNodeLabelVal = "true"
)

var (
	workerNodeCount = -1

	testStorageClasses = []*storagev1.StorageClass{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "diag-test-sc-1",
			},
			Provisioner: pxcomp.PortworxCSIProvisioner,
			Parameters: map[string]string{
				"repl":  "1",
				"nodes": "", // Populated in the test main for more predictable behavior
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "diag-test-sc-2",
			},
			Provisioner: pxcomp.PortworxCSIProvisioner,
			Parameters: map[string]string{
				"repl":  "2",
				"nodes": "", // Populated in the test main for more predictable behavior
			},
		},
	}
	testVolumes = []*v1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc-1",
				Namespace: "default",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				StorageClassName: &testStorageClasses[0].Name,
				AccessModes:      []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
				Resources: v1.ResourceRequirements{
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceStorage: resource.MustParse("4Gi"),
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc-2",
				Namespace: "default",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				StorageClassName: &testStorageClasses[1].Name,
				AccessModes:      []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
				Resources: v1.ResourceRequirements{
					Requests: map[v1.ResourceName]resource.Quantity{
						v1.ResourceStorage: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
)

var testPortworxDiagsCases = []types.TestCase{
	{
		TestName:        "DiagCollection_AllNodes",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-all-nodes",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_AllNodes,
	},
	{
		TestName:        "DiagCollection_NodeLabelSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-node-label-selector",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All:    false,
								Labels: map[string]string{diagNodeLabelKey: diagNodeLabelVal},
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_NodeLabelSelector,
	},
	{
		TestName:        "DiagCollection_NodeIDSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-node-id-selector",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: false,
								IDs: []string{}, // Populated in test function
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_NodeIDSelector,
	},
	{
		TestName:        "DiagCollection_VolumeLabelSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-volume-label-selector",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							VolumeSelector: portworxv1.VolumeSelector{
								Labels: map[string]string{
									diagVolumeLabelKey: diagVolumeLabelVal,
								},
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_VolumeLabelSelector,
	},
	{
		TestName:        "DiagCollection_VolumeIDSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-volume-id-selector",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							VolumeSelector: portworxv1.VolumeSelector{
								IDs: []string{}, // Populated in test function
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_VolumeIDSelector,
	},
	{
		TestName:        "DiagCollection_SecondDiagShouldWait",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-second-diag-should-wait",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_SecondDiagShouldWait,
	},
	{
		TestName:        "DiagCollection_StopNodes",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-stop-nodes",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_StopNodes,
		ShouldSkip: func(tc *types.TestCase) bool {
			return workerNodeCount < 4 // Need at least 3 nodes for quorum + 1 to put labels on
		},
	},
	{
		TestName:        "DiagCollection_DeleteDiagBeforeComplete_Recreate",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-delete-diag-before-complete",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_DeleteDiagBeforeComplete_Recreate,
	},
	{
		TestName:        "DiagCollection_DeleteDiagPods",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-delete-diag-pods",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_DeleteDiagPods,
	},
	{
		TestName:        "DiagCollection_TelemetryDisabled",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-telemetry-disabled",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: true,
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_TelemetryDisabled,
	},
	{
		TestName:        "DiagCollection_NodeIDAndNodeLabelSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-node-id-label",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All:    false,
								Labels: map[string]string{diagNodeLabelKey: diagNodeLabelVal},
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_NodeIDAndNodeLabelSelector,
	},
	{
		TestName:        "DiagCollection_VolumeIDAndVolumeLabelSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-volume-id-label",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: false,
							},
							VolumeSelector: portworxv1.VolumeSelector{
								Labels: map[string]string{diagVolumeLabelKey: diagVolumeLabelVal},
								IDs:    []string{}, // Populated in test function
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_VolumeIDAndVolumeLabelSelector,
	},
	{
		TestName:        "DiagCollection_NodeIDAndVolumeLabelSelector",
		TestrailCaseIDs: []string{},
		TestSpec: func(t *testing.T) interface{} {
			return &diagTestSpec{
				DiagToCreate: &portworxv1.PortworxDiag{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-portworx-diag-node-id-volume-label",
						Namespace: DiagTestNamespace,
					},
					Spec: portworxv1.PortworxDiagSpec{
						Portworx: &portworxv1.PortworxComponent{
							NodeSelector: portworxv1.NodeSelector{
								All: false,
								IDs: []string{}, // Populated in test function
							},
							VolumeSelector: portworxv1.VolumeSelector{
								Labels: map[string]string{diagVolumeLabelKey: diagVolumeLabelVal},
							},
						},
					},
				},
			}
		},
		TestFunc: DiagCollection_NodeIDAndVolumeLabelSelector,
	},
}

func diagCollectionTest(t *testing.T, dtc *diagTestSpec, expectedNodes []corev1.StorageNode) {
	// 1. Create the PortworxDiag object, validate that it eventually goes to Pending
	diag, err := operatorops.Instance().CreatePortworxDiag(dtc.DiagToCreate)
	require.NoError(t, err)

	out, err := task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusPending, portworxv1.DiagStatusInProgress}), time.Minute*1, time.Second*10)
	require.NoError(t, err, "Failed to wait for Portworx Diag to be pending (or in progress)")
	diag = out.(*portworxv1.PortworxDiag)

	// Check that it has a cluster UUID set
	require.Equal(t, dtc.Cluster.Status.ClusterUID, diag.Status.ClusterUUID)

	// 2. Validate that pods are created on the proper nodes.
	_, err = task.DoRetryWithTimeout(validateAllDiagPodsExist(diag, expectedNodes), time.Minute*1, time.Second*10)
	require.NoError(t, err)

	// 3. Validate that PortworxDiag object goes to InProgress, and that PortworxDiag node statuses are updated properly
	out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusInProgress}), time.Minute*2, time.Second*10)
	require.NoError(t, err, "Failed to wait for Portworx Diag to be in progress")
	diag = out.(*portworxv1.PortworxDiag)

	// TODO: validate that an alert is raised when diag collection starts (PWX-32805)

	// Check that the individual node statuses are also in progress (or completed, if it moves fast)
	out, err = task.DoRetryWithTimeout(validatePortworxDiagNodesInPhase(diag, expectedNodes, []string{portworxv1.DiagStatusInProgress, portworxv1.DiagStatusCompleted}), time.Minute, time.Second*10)
	require.NoError(t, err, "Failed to wait for Portworx Diag nodes to be in progress (or completed)")
	diag = out.(*portworxv1.PortworxDiag)

	// 4. Wait for PortworxDiag to complete
	out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusCompleted}), time.Minute*5, time.Second*15)
	require.NoError(t, err)
	diag = out.(*portworxv1.PortworxDiag)

	out, err = task.DoRetryWithTimeout(validatePortworxDiagNodesInPhase(diag, expectedNodes, []string{portworxv1.DiagStatusCompleted}), time.Minute, time.Second*15)
	require.NoError(t, err, "Failed to wait for PortworxDiag nodes to be completed")
	diag = out.(*portworxv1.PortworxDiag)

	// Validate that all node statuses include a path to the diag tarball
	for _, s := range diag.Status.NodeStatuses {
		require.True(t, strings.Contains(s.Message, "px-diags"), "Node status message does not contain path to diag tarball (px-diags)")
	}

	// TODO: validate that an alert is raised when diag collection succeeds (PWX-32805)

	_, err = task.DoRetryWithTimeout(validateDiagPodsDeleted(), time.Minute, time.Second*15)
	require.NoError(t, err, "Failed to wait for PortworxDiag pods to be deleted")

	// 5. Delete the PortworxDiag
	err = operatorops.Instance().DeletePortworxDiag(diag.Name, diag.Namespace)
	require.NoError(t, err)
}

func DiagCollection_AllNodes(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		diagCollectionTest(t, dtc, dtc.StorageNodes)
	}
}

func DiagCollection_NodeLabelSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Label the k8s nodes (should only run on nodes 0 and 1)
		require.NoError(t, coreops.Instance().AddLabelOnNode(dtc.StorageNodes[0].Name, diagNodeLabelKey, diagNodeLabelVal))
		require.NoError(t, coreops.Instance().AddLabelOnNode(dtc.StorageNodes[1].Name, diagNodeLabelKey, diagNodeLabelVal))
		require.NoError(t, coreops.Instance().AddLabelOnNode(dtc.StorageNodes[2].Name, diagNodeLabelKey, "wrongval"))

		diagCollectionTest(t, dtc, []corev1.StorageNode{dtc.StorageNodes[0], dtc.StorageNodes[1]})

		// Unlabel the nodes
		require.NoError(t, coreops.Instance().RemoveLabelOnNode(dtc.StorageNodes[0].Name, diagNodeLabelKey))
		require.NoError(t, coreops.Instance().RemoveLabelOnNode(dtc.StorageNodes[1].Name, diagNodeLabelKey))
		require.NoError(t, coreops.Instance().RemoveLabelOnNode(dtc.StorageNodes[2].Name, diagNodeLabelKey))
	}
}

func DiagCollection_NodeIDSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Set the node IDs we're selecting for
		dtc.DiagToCreate.Spec.Portworx.NodeSelector.IDs = []string{dtc.StorageNodes[0].Status.NodeUID, dtc.StorageNodes[1].Status.NodeUID}

		diagCollectionTest(t, dtc, []corev1.StorageNode{dtc.StorageNodes[0], dtc.StorageNodes[1]})
	}
}

func DiagCollection_VolumeLabelSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Copy storage classes and set node IDs for more predictable behavior
		testSTC0 := testStorageClasses[0].DeepCopy()
		testSTC0.Parameters[diagVolumeLabelKey] = diagVolumeLabelVal
		testSTC0.Parameters["nodes"] = dtc.StorageNodes[0].Status.NodeUID // Repl1 volume
		testSTC1 := testStorageClasses[1].DeepCopy()
		testSTC0.Parameters[diagVolumeLabelKey] = diagVolumeLabelVal
		testSTC1.Parameters["nodes"] = fmt.Sprintf("%s,%s", dtc.StorageNodes[0].Status.NodeUID, dtc.StorageNodes[1].Status.NodeUID) // Repl2 volume
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testSTC0, testSTC1}))

		// Create some test volumes with labels to test volume selectors: storage class name is pre-populated
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))

		// Wait for PVCs to be bound
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[0], time.Minute, time.Second*5))
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[1], time.Minute, time.Second*5))

		// Volume label selector takes it from here... run test!
		diagCollectionTest(t, dtc, []corev1.StorageNode{dtc.StorageNodes[0], dtc.StorageNodes[1]})

		// Delete the test volumes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))
		// Delete the test storage classes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testSTC0, testSTC1}))
	}
}

func DiagCollection_VolumeIDSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Copy storage classes and set node IDs for more predictable behavior
		testSTC0 := testStorageClasses[0].DeepCopy()
		testSTC0.Parameters["nodes"] = dtc.StorageNodes[0].Status.NodeUID // Repl1 volume
		testSTC1 := testStorageClasses[1].DeepCopy()
		testSTC1.Parameters["nodes"] = fmt.Sprintf("%s,%s", dtc.StorageNodes[0].Status.NodeUID, dtc.StorageNodes[1].Status.NodeUID) // Repl2 volume
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testSTC0, testSTC1}))

		// Create some test volumes with labels to test volume selectors: storage class name is pre-populated
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))

		// Wait for PVCs to be bound
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[0], time.Minute, time.Second*5))
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[1], time.Minute, time.Second*5))

		// Get PVs for PVCs to get volume IDs
		var err error
		testVolumes[0], err = coreops.Instance().GetPersistentVolumeClaim(testVolumes[0].Name, testVolumes[0].Namespace)
		require.NoError(t, err)
		require.NotEmpty(t, testVolumes[0].Spec.VolumeName)
		testVolumes[1], err = coreops.Instance().GetPersistentVolumeClaim(testVolumes[1].Name, testVolumes[1].Namespace)
		require.NoError(t, err)
		require.NotEmpty(t, testVolumes[1].Spec.VolumeName)

		// Set the volume IDs we're selecting for
		dtc.DiagToCreate.Spec.Portworx.VolumeSelector.IDs = []string{testVolumes[0].Spec.VolumeName, testVolumes[1].Spec.VolumeName}

		diagCollectionTest(t, dtc, []corev1.StorageNode{dtc.StorageNodes[0], dtc.StorageNodes[1]})

		// Delete the test volumes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))
		// Delete the test storage classes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testSTC0, testSTC1}))
	}
}

func DiagCollection_SecondDiagShouldWait(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		firstDiag := dtc.DiagToCreate.DeepCopy()
		firstDiag.Name += "-first"

		secondDiag := dtc.DiagToCreate.DeepCopy()
		secondDiag.Name += "-second"

		// 1. Create the first PortworxDiag object, validate that it eventually goes to In Progress
		firstDiag, err := operatorops.Instance().CreatePortworxDiag(firstDiag)
		require.NoError(t, err)

		out, err := task.DoRetryWithTimeout(validatePortworxDiagInPhase(firstDiag, []string{portworxv1.DiagStatusInProgress}), time.Minute*1, time.Second*10)
		require.NoError(t, err, "Failed to wait for first Portworx Diag to be in progress")
		firstDiag = out.(*portworxv1.PortworxDiag)

		// 2. Create a second PortworxDiag object, validate that it goes to pending
		secondDiag, err = operatorops.Instance().CreatePortworxDiag(secondDiag)
		require.NoError(t, err)

		out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(secondDiag, []string{portworxv1.DiagStatusPending}), time.Minute*1, time.Second*10)
		require.NoError(t, err, "Failed to wait for second Portworx Diag to be pending")
		secondDiag = out.(*portworxv1.PortworxDiag)

		// 3. Wait for first PortworxDiag to complete
		out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(firstDiag, []string{portworxv1.DiagStatusCompleted}), time.Minute*5, time.Second*15)
		require.NoError(t, err)
		firstDiag = out.(*portworxv1.PortworxDiag)

		// 4. Validate cleanup of the first PortworxDiag (but don't yet delete)
		_, err = task.DoRetryWithTimeout(validateDiagPodsDeleted(), time.Minute, time.Second*15)
		require.NoError(t, err, "Failed to wait for PortworxDiag pods to be deleted")

		// 5. Validate that the second PortworxDiag is in progress
		out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(secondDiag, []string{portworxv1.DiagStatusInProgress}), time.Minute*1, time.Second*10)
		require.NoError(t, err, "Failed to wait for second Portworx Diag to be in progress")
		secondDiag = out.(*portworxv1.PortworxDiag)

		// 6. Clean up (don't bother waiting for the second diag to finish)
		err = operatorops.Instance().DeletePortworxDiag(secondDiag.Name, secondDiag.Namespace)
		require.NoError(t, err)
		err = operatorops.Instance().DeletePortworxDiag(firstDiag.Name, firstDiag.Namespace)
		require.NoError(t, err)
	}
}

func DiagCollection_StopNodes(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		stoppedNode := ci_utils.AddLabelToRandomNode(t, "px/service", "stop")

		_, err := task.DoRetryWithTimeout(validateStorageNodeInState(stoppedNode, dtc.DiagToCreate.Namespace, corev1.NodeOfflineStatus), time.Minute*2, time.Second*15)
		require.NoError(t, err)

		diagCollectionTest(t, dtc, dtc.StorageNodes)

		ci_utils.RemoveLabelFromNode(t, stoppedNode, "px/service")

		_, err = task.DoRetryWithTimeout(validateStorageNodeInState(stoppedNode, dtc.DiagToCreate.Namespace, corev1.NodeOnlineStatus), time.Minute*5, time.Second*15)
		require.NoError(t, err)
	}
}

func DiagCollection_DeleteDiagBeforeComplete_Recreate(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// 1. Create the PortworxDiag object, validate that it eventually goes to Pending
		diag, err := operatorops.Instance().CreatePortworxDiag(dtc.DiagToCreate)
		require.NoError(t, err)

		out, err := task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusInProgress}), time.Minute*1, time.Second*10)
		require.NoError(t, err, "Failed to wait for Portworx Diag to be in progress")
		diag = out.(*portworxv1.PortworxDiag)

		// 2. Validate that pods are created on the proper nodes.
		_, err = task.DoRetryWithTimeout(validateAllDiagPodsExist(diag, dtc.StorageNodes), time.Minute*1, time.Second*10)
		require.NoError(t, err)

		// 3. Delete the PortworxDiag, validate that all pods go away
		err = operatorops.Instance().DeletePortworxDiag(diag.Name, diag.Namespace)
		require.NoError(t, err)

		_, err = task.DoRetryWithTimeout(validateDiagPodsDeleted(), time.Minute, time.Second*15)
		require.NoError(t, err, "Failed to wait for PortworxDiag pods to be deleted")

		// 4. Re-run the test, validate that everything else goes as planned
		diagCollectionTest(t, dtc, dtc.StorageNodes)
	}
}

func DiagCollection_DeleteDiagPods(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// 1. Create the PortworxDiag object, validate that it eventually goes to Pending
		diag, err := operatorops.Instance().CreatePortworxDiag(dtc.DiagToCreate)
		require.NoError(t, err)

		out, err := task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusPending, portworxv1.DiagStatusInProgress}), time.Minute*1, time.Second*10)
		require.NoError(t, err, "Failed to wait for Portworx Diag to be pending (or in progress)")
		diag = out.(*portworxv1.PortworxDiag)

		// Check that it has a cluster UUID set
		require.Equal(t, dtc.Cluster.Status.ClusterUID, diag.Status.ClusterUUID)

		// 2. Validate that pods are created on the proper nodes.
		_, err = task.DoRetryWithTimeout(validateAllDiagPodsExist(diag, dtc.StorageNodes), time.Minute*1, time.Second*10)
		require.NoError(t, err)

		// 3. Validate that PortworxDiag object goes to InProgress, and that PortworxDiag node statuses are updated properly
		out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusInProgress}), time.Minute*2, time.Second*10)
		require.NoError(t, err, "Failed to wait for Portworx Diag to be in progress")
		diag = out.(*portworxv1.PortworxDiag)

		// 4. Delete all the diag pods that exist
		err = coreops.Instance().DeletePodsByLabels(diag.Namespace, map[string]string{"name": portworxdiag.PortworxDiagLabel}, time.Second*30)
		require.NoError(t, err)

		// Validate that the pods come back
		_, err = task.DoRetryWithTimeout(validateAllDiagPodsExist(diag, dtc.StorageNodes), time.Minute*1, time.Second*10)
		require.NoError(t, err)

		// 5. Wait for PortworxDiag to complete
		out, err = task.DoRetryWithTimeout(validatePortworxDiagInPhase(diag, []string{portworxv1.DiagStatusCompleted}), time.Minute*5, time.Second*15)
		require.NoError(t, err)
		diag = out.(*portworxv1.PortworxDiag)

		out, err = task.DoRetryWithTimeout(validatePortworxDiagNodesInPhase(diag, dtc.StorageNodes, []string{portworxv1.DiagStatusCompleted}), time.Minute, time.Second*15)
		require.NoError(t, err, "Failed to wait for PortworxDiag nodes to be completed")
		diag = out.(*portworxv1.PortworxDiag)

		// Validate that all node statuses include a path to the diag tarball
		for _, s := range diag.Status.NodeStatuses {
			require.True(t, strings.Contains(s.Message, "px-diags"), "Node status message does not contain path to diag tarball (px-diags)")
		}

		_, err = task.DoRetryWithTimeout(validateDiagPodsDeleted(), time.Minute, time.Second*15)
		require.NoError(t, err, "Failed to wait for PortworxDiag pods to be deleted")

		// 6. Delete the PortworxDiag
		err = operatorops.Instance().DeletePortworxDiag(diag.Name, diag.Namespace)
		require.NoError(t, err)
	}
}

func DiagCollection_TelemetryDisabled(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		modified := false

		// Patch STC to have telemetry disabled
		if dtc.Cluster.Spec.Monitoring != nil && dtc.Cluster.Spec.Monitoring.Telemetry != nil && dtc.Cluster.Spec.Monitoring.Telemetry.Enabled {
			logrus.Infof("Disabling telemetry for STC [%s]", dtc.Cluster.Name)
			modified = true
			_ = ci_utils.UpdateAndValidateStorageCluster(dtc.Cluster, func(existing *corev1.StorageCluster) *corev1.StorageCluster {
				existing.Spec.Monitoring.Telemetry.Enabled = false
				return existing
			}, ci_utils.PxSpecImages, t)
		}

		diagCollectionTest(t, dtc, dtc.StorageNodes)

		// Reset STC if we changed it
		if modified {
			logrus.Infof("Resetting STC [%s] to have telemetry enabled", dtc.Cluster.Name)
			_ = ci_utils.UpdateAndValidateStorageCluster(dtc.Cluster, func(existing *corev1.StorageCluster) *corev1.StorageCluster {
				existing.Spec.Monitoring.Telemetry.Enabled = true
				return existing
			}, ci_utils.PxSpecImages, t)
		}
	}
}

func DiagCollection_NodeIDAndNodeLabelSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Label the k8s nodes
		require.NoError(t, coreops.Instance().AddLabelOnNode(dtc.StorageNodes[0].Name, diagNodeLabelKey, diagNodeLabelVal))
		require.NoError(t, coreops.Instance().AddLabelOnNode(dtc.StorageNodes[1].Name, diagNodeLabelKey, diagNodeLabelVal))

		// Set the node IDs we're selecting for
		dtc.DiagToCreate.Spec.Portworx.NodeSelector.IDs = []string{dtc.StorageNodes[2].Status.NodeUID}

		// Should only run on nodes 0-2: nodes 0+1 by label, node 2 by ID
		diagCollectionTest(t, dtc, dtc.StorageNodes[0:2])

		// Unlabel the nodes
		require.NoError(t, coreops.Instance().RemoveLabelOnNode(dtc.StorageNodes[0].Name, diagNodeLabelKey))
		require.NoError(t, coreops.Instance().RemoveLabelOnNode(dtc.StorageNodes[1].Name, diagNodeLabelKey))
	}
}

func DiagCollection_VolumeIDAndVolumeLabelSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Copy storage classes and set node IDs for more predictable behavior
		testSTC0 := testStorageClasses[0].DeepCopy()
		testSTC0.Parameters[diagVolumeLabelKey] = diagVolumeLabelVal // Only label one STC
		testSTC0.Parameters["nodes"] = dtc.StorageNodes[0].Status.NodeUID
		testSTC1 := testStorageClasses[1].DeepCopy() // Second volume will be selected by ID
		testSTC1.Parameters["nodes"] = fmt.Sprintf("%s,%s", dtc.StorageNodes[1].Status.NodeUID, dtc.StorageNodes[2].Status.NodeUID)
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testSTC0, testSTC1}))

		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))

		// Wait for PVCs to be bound
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[0], time.Minute, time.Second*5))
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[1], time.Minute, time.Second*5))

		// Get PVCs to get volume IDs
		var err error
		testVolumes[1], err = coreops.Instance().GetPersistentVolumeClaim(testVolumes[1].Name, testVolumes[1].Namespace)
		require.NoError(t, err)
		require.NotEmpty(t, testVolumes[1].Spec.VolumeName)

		// Set the volume IDs we're selecting for
		dtc.DiagToCreate.Spec.Portworx.VolumeSelector.IDs = []string{testVolumes[1].Spec.VolumeName}

		diagCollectionTest(t, dtc, []corev1.StorageNode{dtc.StorageNodes[0], dtc.StorageNodes[1]})

		// Delete the test volumes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testVolumes[0], testVolumes[1]}))
		// Delete the test storage classes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testSTC0, testSTC1}))
	}
}

func DiagCollection_NodeIDAndVolumeLabelSelector(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		dtc, ok := testSpec.(*diagTestSpec)
		require.True(t, ok)

		// Copy storage classes and set node IDs for more predictable behavior
		testSTC := testStorageClasses[0].DeepCopy() // Volume will be selected by label
		testSTC.Parameters[diagVolumeLabelKey] = diagVolumeLabelVal
		testSTC.Parameters["nodes"] = dtc.StorageNodes[0].Status.NodeUID

		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testSTC}))
		require.NoError(t, ci_utils.CreateObjects([]runtime.Object{testVolumes[0]}))

		// Wait for PVCs to be bound
		require.NoError(t, coreops.Instance().ValidatePersistentVolumeClaim(testVolumes[0], time.Minute, time.Second*5))

		// Get PVC to get volume ID
		var err error
		testVolumes[0], err = coreops.Instance().GetPersistentVolumeClaim(testVolumes[0].Name, testVolumes[0].Namespace)
		require.NoError(t, err)
		require.NotEmpty(t, testVolumes[0].Spec.VolumeName)

		// Set the node ID we're selecting for
		dtc.DiagToCreate.Spec.Portworx.NodeSelector.IDs = []string{dtc.StorageNodes[1].Status.NodeUID}

		diagCollectionTest(t, dtc, dtc.StorageNodes[0:1])

		// Delete the test volumes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testVolumes[0]}))
		// Delete the test storage classes
		require.NoError(t, ci_utils.DeleteObjects([]runtime.Object{testSTC}))
	}
}

func validateStorageNodeInState(nodeName, namespace string, state corev1.NodeConditionStatus) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		node, err := operatorops.Instance().GetStorageNode(nodeName, namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get node %s, Err: %v", nodeName, err)
		}
		if node == nil {
			return nil, true, fmt.Errorf("node %s not found", nodeName)
		}
		if node.Status.Phase != string(state) {
			return nil, true, fmt.Errorf("node %s not in state %s", nodeName, state)
		}
		logrus.Infof("Node [%s] is in state [%s]", nodeName, state)
		return node, false, nil
	}
}

func validateDiagPodsDeleted() func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		pods, err := coreops.Instance().ListPods(map[string]string{
			"name": portworxdiag.PortworxDiagLabel,
		})
		if err != nil {
			return nil, true, fmt.Errorf("failed to list pods, Err: %v", err)
		}
		if len(pods.Items) > 0 {
			podNames := []string{}
			for _, p := range pods.Items {
				podNames = append(podNames, p.Name)
			}
			return nil, true, fmt.Errorf("diag pods still exist: [%s]", strings.Join(podNames, " "))
		}
		logrus.Info("All diag pods deleted")
		return nil, false, nil
	}
}

func validatePortworxDiagInPhase(diag *portworxv1.PortworxDiag, validPhases []string) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		diag, err := operatorops.Instance().GetPortworxDiag(diag.Name, diag.Namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get PortworxDiag [%s] in [%s], Err: %v", diag.Name, diag.Namespace, err)
		}
		if diag.Status.Phase == "" {
			return nil, true, fmt.Errorf("failed to get Portworx diag status")
		}
		isValid := false
		for _, phase := range validPhases {
			if diag.Status.Phase == phase {
				isValid = true
			}
		}
		if !isValid {
			return nil, true, fmt.Errorf("diag status: [%s], expected status to be one of: %v", diag.Status.Phase, validPhases)
		}

		logrus.Infof("PortworxDiag [%s] is [%s]", diag.Name, diag.Status.Phase)
		return diag, false, nil
	}
}

func validatePortworxDiagNodesInPhase(diag *portworxv1.PortworxDiag, expectedStorageNodes []corev1.StorageNode, validPhases []string) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		d, err := operatorops.Instance().GetPortworxDiag(diag.Name, diag.Namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get PortworxDiag [%s] in [%s], Err: %v", diag.Name, diag.Namespace, err)
		}
		if len(d.Status.NodeStatuses) != len(expectedStorageNodes) {
			return nil, true, fmt.Errorf("PortworxDiag only has %d node statuses, expected %d", len(d.Status.NodeStatuses), len(expectedStorageNodes))
		}
		for _, nodeStatus := range d.Status.NodeStatuses {
			isValid := false
			for _, phase := range validPhases {
				if nodeStatus.Status == phase {
					isValid = true
				}
			}
			if !isValid {
				return nil, true, fmt.Errorf("diag node %s status: [%s], expected status to be one of: %v", nodeStatus.NodeID, nodeStatus.Status, validPhases)
			}
		}

		logrus.Infof("PortworxDiag [%s]: all nodes are one of %v", d.Name, validPhases)
		return d, false, nil
	}
}

func validateAllDiagPodsExist(diag *portworxv1.PortworxDiag, expectedStorageNodes []corev1.StorageNode) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		nodeIDToPod, err := getNodeIDToDiagPodMap()
		if err != nil {
			return nil, true, fmt.Errorf("failed to get node ID to pod map, Err: %v", err)
		}
		if len(nodeIDToPod) != len(expectedStorageNodes) {
			return nil, true, fmt.Errorf("number of diag pods does not match number of expected storage nodes: expected %d, got %d", len(expectedStorageNodes), len(nodeIDToPod))
		}
		for _, s := range diag.Status.NodeStatuses {
			_, ok := nodeIDToPod[s.NodeID]
			if !ok {
				return nil, true, fmt.Errorf("failed to find pod for node [%s]", s.NodeID)
			}
		}
		logrus.Infof("All %d pods exist for PortworxDiag [%s]", len(expectedStorageNodes), diag.Name)
		return nil, false, nil
	}
}

func getNodeIDToDiagPodMap() (map[string]v1.Pod, error) {
	// List all pods by label selector
	pods, err := coreops.Instance().ListPods(map[string]string{
		"name": portworxdiag.PortworxDiagLabel,
	})
	if err != nil {
		return nil, err
	}

	nodeIDToPod := map[string]v1.Pod{}
	for _, pod := range pods.Items {
		// Extract the node ID from the pod argument list
		nodeID := ""
		for i, arg := range pod.Spec.Containers[0].Args {
			if strings.EqualFold(arg, "--diags-node-id") {
				nodeID = pod.Spec.Containers[0].Args[i+1]
				break
			}
		}
		if nodeID == "" {
			return nil, fmt.Errorf("failed to find node ID in pod args for diag pod [%s]", pod.Name)
		}
		logrus.Infof("Found diag pod [%s] for node [%s]", pod.Name, nodeID)
		nodeIDToPod[nodeID] = pod
	}
	return nodeIDToPod, nil
}

// injectRuntimeDataIntoTestSpec will add extra data into the TestSpec that is not known at compile-time,
// but can be used to reduce number of API calls required
func injectRuntimeDataIntoTestSpec(testCase *types.TestCase, cluster *corev1.StorageCluster, storageNodes []corev1.StorageNode) func(t *testing.T) interface{} {
	innerTestSpec := testCase.TestSpec
	return func(t *testing.T) interface{} {
		testSpecResult := innerTestSpec(t)
		diagTestSpec, ok := testSpecResult.(*diagTestSpec)
		require.True(t, ok, "Diag test case [%s]'s test spec is not a *DiagTestSpec", testCase.TestName)

		diagTestSpec.Cluster = cluster
		diagTestSpec.StorageNodes = storageNodes

		return diagTestSpec
	}
}

func ensureNodeMarkedDisabled(t *testing.T) string {
	allNodes, err := coreops.Instance().GetNodes()
	require.NoError(t, err)

	workerNodes := []v1.Node{}
	hasDisabled := false
	for _, node := range allNodes.Items {
		if val, ok := node.Labels["px/enabled"]; ok && val == "false" {
			hasDisabled = true
			continue
		}
		if !coreops.Instance().IsNodeMaster(node) {
			workerNodes = append(workerNodes, node)
		}
	}
	workerNodeCount = len(workerNodes)
	logrus.Infof("Found %d worker nodes for diag tests", workerNodeCount)

	if len(workerNodes) > 3 && !hasDisabled {
		// Label a random node as disabled to make sure diags don't run on it
		labeledNode := ci_utils.AddLabelToRandomNode(t, "px/enabled", "false")
		logrus.Infof("Labeled node [%s] as disabled", labeledNode)
		return labeledNode
	}
	return ""
}

func TestPortworxDiags(t *testing.T) {
	// Do our best to try disabling one node
	ensureNodeMarkedDisabled(t)

	// Deploy a storage cluster for us to run diags against
	cluster := &corev1.StorageCluster{
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
		},
	}
	cluster.Name = "portworx-diag-test"
	err := ci_utils.ConstructStorageCluster(cluster, ci_utils.PxSpecGenURL, ci_utils.PxSpecImages)
	require.NoError(t, err)
	cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)

	// Re-fetch latest cluster status
	cluster, err = operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	require.NoError(t, err)

	// List storage nodes
	storageNodes, err := operatorops.Instance().ListStorageNodes(cluster.Namespace)
	require.NoError(t, err)

	for _, testCase := range testPortworxDiagsCases {
		testCase.TestSpec = injectRuntimeDataIntoTestSpec(&testCase, cluster, storageNodes.Items)
		testCase.RunTest(t)
	}

	// Wipe PX and validate
	ci_utils.UninstallAndValidateStorageCluster(cluster, t)
}
