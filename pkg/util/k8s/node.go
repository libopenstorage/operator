package k8s

import (
	"context"
	"fmt"

	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
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
	if machineName, present := node.Annotations[constants.ClusterAPIMachineAnnotation]; present && len(machineName) > 0 {
		machine := &cluster_v1alpha1.Machine{}
		err := cl.Get(context.TODO(), client.ObjectKey{Name: machineName, Namespace: "default"}, machine)
		if err != nil {
			return false, fmt.Errorf("failed to get machine: default/%s due to: %v", machineName, err)
		} else {
			if machine.GetDeletionTimestamp() != nil {
				logrus.Debugf("machine: %s is being deleted. timestamp set: %v.",
					machineName, machine.GetDeletionTimestamp())
				return true, nil
			}
		}
	}
	return false, nil
}
