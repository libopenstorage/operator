package k8s

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/api"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

/*
file contains node level k8s utility functions
*/

// IsNodeBeingDeleted returns true if the underlying machine for the Kubernetes node is being deleted.
// This method is only supported on platforms that use the cluster-api (https://github.com/kubernetes-sigs/cluster-api)
func IsNodeBeingDeleted(node *v1.Node, cl client.Client) (bool, error) {
	// check if node is managed by a cluster API machine and if the machine is marked for deletion
	if machineName, present := node.Annotations[constants.AnnotationClusterAPIMachine]; present && len(machineName) > 0 {
		machine := &cluster_v1alpha1.Machine{}
		err := cl.Get(context.TODO(), client.ObjectKey{Name: machineName, Namespace: "default"}, machine)
		if err != nil {
			return false, fmt.Errorf("failed to get machine: default/%s due to: %v", machineName, err)
		}

		if machine.GetDeletionTimestamp() != nil {
			logrus.Debugf("machine: %s is being deleted. timestamp set: %v.",
				machineName, machine.GetDeletionTimestamp())
			return true, nil
		}
	}
	return false, nil
}

// IsNodeRecentlyCordoned returns true if the given node is cordoned within the
// default delay or the user provided delay in given StorageCluster object
func IsNodeRecentlyCordoned(
	node *v1.Node,
	cluster *corev1.StorageCluster,
) bool {
	cordoned, startTime := IsNodeCordoned(node)
	if !cordoned || startTime.IsZero() {
		return false
	}

	var waitDuration time.Duration
	if duration, err := strconv.Atoi(cluster.Annotations[constants.AnnotationCordonedRestartDelay]); err == nil {
		waitDuration = time.Duration(duration) * time.Second
	} else {
		waitDuration = constants.DefaultCordonedRestartDelay
	}
	return time.Now().Add(-waitDuration).Before(startTime)
}

// IsNodeCordoned returns true if the given noode is marked unschedulable. It
// also returns the time when the node was cordoned if available in the node
// taints.
func IsNodeCordoned(node *v1.Node) (bool, time.Time) {
	if node.Spec.Unschedulable {
		for _, taint := range node.Spec.Taints {
			if taint.Key == api.TaintNodeUnschedulable {
				if taint.TimeAdded != nil {
					return true, taint.TimeAdded.Time
				}
				break
			}
		}
		return true, time.Time{}
	}
	return false, time.Time{}
}
