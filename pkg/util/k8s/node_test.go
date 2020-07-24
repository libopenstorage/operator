package k8s

import (
	"testing"

	"github.com/libopenstorage/operator/pkg/constants"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
