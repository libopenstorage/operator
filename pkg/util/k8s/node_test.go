package k8s

import (
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/api"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
)

func TestIsNodeBeingDeleted(t *testing.T) {
	// not controlled by machine, not being deleted
	n1 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "n1",
		},
		Status: v1.NodeStatus{
			Phase: v1.NodeRunning,
		},
	}

	n2 := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "n2",
			Annotations: map[string]string{
				constants.AnnotationClusterAPIMachine: "m2",
			},
		},
		Status: v1.NodeStatus{
			Phase: v1.NodeRunning,
		},
	}

	now := metav1.Now()
	m2 := &cluster_v1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "m2",
			Namespace:         "default",
			DeletionTimestamp: &now,
		},
	}

	k8sClient := testutil.FakeK8sClient(n1, n2, m2)

	// n1
	isBeingDeleted, err := IsNodeBeingDeleted(n1, k8sClient)
	require.NoError(t, err)
	require.False(t, isBeingDeleted)

	// n2,m2
	isBeingDeleted, err = IsNodeBeingDeleted(n2, k8sClient)
	require.NoError(t, err)
	require.True(t, isBeingDeleted)

	n2.Annotations[constants.AnnotationClusterAPIMachine] = "no-such-machine"
	isBeingDeleted, err = IsNodeBeingDeleted(n2, k8sClient)
	require.Error(t, err)
	require.False(t, isBeingDeleted)
}

func TestIsNodeCordoned(t *testing.T) {
	// TestCase: Not marked as unschedulable
	node := &v1.Node{}

	cordoned, startTime := IsNodeCordoned(node)

	require.False(t, cordoned)
	require.True(t, startTime.IsZero())

	// TestCase: Marked as unschedulable but no startTime
	node.Spec.Unschedulable = true

	cordoned, startTime = IsNodeCordoned(node)

	require.True(t, cordoned)
	require.True(t, startTime.IsZero())

	// TestCase: Marked as unschedulable but Unschedulable taint not present
	node.Spec.Taints = []v1.Taint{}

	cordoned, startTime = IsNodeCordoned(node)

	require.True(t, cordoned)
	require.True(t, startTime.IsZero())

	// TestCase: Marked as unschedulable but Unschedulable taint without timeAdded
	taint := v1.Taint{
		Key: api.TaintNodeUnschedulable,
	}
	node.Spec.Taints = append(node.Spec.Taints, taint)

	cordoned, startTime = IsNodeCordoned(node)

	require.True(t, cordoned)
	require.True(t, startTime.IsZero())

	// TestCase: Marked as unschedulable with timeAdded
	timeAdded := metav1.Now()
	node.Spec.Taints[0].TimeAdded = &timeAdded

	cordoned, startTime = IsNodeCordoned(node)

	require.True(t, cordoned)
	require.False(t, startTime.IsZero())
	require.Equal(t, timeAdded.Time, startTime)
}

func TestIsNodeRecentlyCordoned(t *testing.T) {
	// TestCase: Node not cordoned
	node := &v1.Node{}
	cluster := &corev1.StorageCluster{}

	recentlyCordoned := IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)

	// TestCase: Node cordoned, but time of cordon is zero
	node.Spec.Unschedulable = true

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)

	// TestCase: Node cordoned, but time of cordon is zero
	node.Spec.Taints = []v1.Taint{
		{
			Key:       api.TaintNodeUnschedulable,
			TimeAdded: nil,
		},
	}

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)

	// TestCase: Cordon time is older than default restart wait duration
	timeAdded := metav1.NewTime(
		metav1.Now().
			Add(-constants.DefaultCordonedRestartDelay).
			Add(-time.Second),
	)
	node.Spec.Taints[0].TimeAdded = &timeAdded

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)

	// TestCase: Cordon time is newer than default restart wait duration
	timeAdded = metav1.NewTime(
		metav1.Now().
			Add(-constants.DefaultCordonedRestartDelay).
			Add(time.Second),
	)

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.True(t, recentlyCordoned)

	// TestCase: Cordon time is older than overriden restart wait duration
	cluster.Annotations = map[string]string{
		constants.AnnotationCordonedRestartDelay: "10",
	}

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)

	// TestCase: Cordon time is newer than overriden restart wait duration
	timeAdded = metav1.NewTime(metav1.Now().Add(-5 * time.Second))

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.True(t, recentlyCordoned)

	// TestCase: Failure to parse the annotation will result in using default delay
	cluster.Annotations[constants.AnnotationCordonedRestartDelay] = "invalid"
	timeAdded = metav1.NewTime(
		metav1.Now().
			Add(-constants.DefaultCordonedRestartDelay).
			Add(time.Second),
	)

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.True(t, recentlyCordoned)

	timeAdded = metav1.NewTime(
		metav1.Now().
			Add(-constants.DefaultCordonedRestartDelay).
			Add(-time.Second),
	)

	recentlyCordoned = IsNodeRecentlyCordoned(node, cluster)

	require.False(t, recentlyCordoned)
}
