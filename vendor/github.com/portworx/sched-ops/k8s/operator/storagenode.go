package operator

import (
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StorageNodeOps is an interface to perfrom k8s StorageNode operations
type StorageNodeOps interface {
	// CreateStorageNode creates the given StorageNode
	CreateStorageNode(*corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error)
	// UpdateStorageNode updates the given StorageNode
	UpdateStorageNode(*corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error)
	// GetStorageNode gets the StorageNode with given name and namespace
	GetStorageNode(string, string) (*corev1alpha1.StorageNode, error)
	// ListStorageNodes lists all the StorageNodes
	ListStorageNodes(string) (*corev1alpha1.StorageNodeList, error)
	// DeleteStorageNode deletes the given StorageNode
	DeleteStorageNode(string, string) error
	// UpdateStorageNodeStatus update the status of given StorageNode
	UpdateStorageNodeStatus(*corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error)
	// UpdateStorageNodeCondition updates or creates the given condition in node status.
	// Returns true if the condition is new or was changed.
	UpdateStorageNodeCondition(*corev1alpha1.NodeStatus, *corev1alpha1.NodeCondition) bool
}

// CreateStorageNode creates the given StorageNode
func (c *Client) CreateStorageNode(node *corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	ns := node.Namespace
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	return c.ost.CoreV1alpha1().StorageNodes(ns).Create(node)
}

// UpdateStorageNode updates the given StorageNode
func (c *Client) UpdateStorageNode(node *corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	return c.ost.CoreV1alpha1().StorageNodes(node.Namespace).Update(node)
}

// GetStorageNode gets the StorageNode with given name and namespace
func (c *Client) GetStorageNode(name, namespace string) (*corev1alpha1.StorageNode, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}
	return c.ost.CoreV1alpha1().StorageNodes(namespace).Get(name, metav1.GetOptions{})
}

// ListStorageNodes lists all the StorageNodes
func (c *Client) ListStorageNodes(namespace string) (*corev1alpha1.StorageNodeList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}
	return c.ost.CoreV1alpha1().StorageNodes(namespace).List(metav1.ListOptions{})
}

// DeleteStorageNode deletes the given StorageNode
func (c *Client) DeleteStorageNode(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	// TODO Temporary removing PropagationPolicy: &deleteForegroundPolicy from metav1.DeleteOptions{}, until we figure out the correct policy to use
	return c.ost.CoreV1alpha1().StorageNodes(namespace).Delete(name, &metav1.DeleteOptions{})
}

// UpdateStorageNodeStatus update the status of given StorageNode
func (c *Client) UpdateStorageNodeStatus(node *corev1alpha1.StorageNode) (*corev1alpha1.StorageNode, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}
	return c.ost.CoreV1alpha1().StorageNodes(node.Namespace).UpdateStatus(node)
}

// UpdateStorageNodeCondition updates or creates the given condition in node status.
// Returns true if the condition is new or was changed.
func (c *Client) UpdateStorageNodeCondition(
	status *corev1alpha1.NodeStatus,
	condition *corev1alpha1.NodeCondition,
) bool {
	condition.LastTransitionTime = metav1.Now()

	conditionIndex, oldCondition := getStorageNodeCondition(status, condition.Type)

	if oldCondition == nil {
		status.Conditions = append(status.Conditions, *condition)
		return true
	}

	if condition.Status == oldCondition.Status {
		condition.LastTransitionTime = oldCondition.LastTransitionTime
	}

	isEqual := condition.Status == oldCondition.Status &&
		condition.Reason == oldCondition.Reason &&
		condition.Message == oldCondition.Message &&
		condition.LastTransitionTime.Equal(&oldCondition.LastTransitionTime)

	status.Conditions[conditionIndex] = *condition
	// Return true if one of the fields have changed.
	return !isEqual
}

// getStorageNodeCondition returns the index and the condition based on the type
// from the given node status
func getStorageNodeCondition(
	status *corev1alpha1.NodeStatus,
	conditionType corev1alpha1.NodeConditionType,
) (int, *corev1alpha1.NodeCondition) {
	if status == nil {
		return -1, nil
	}
	return getStorageNodeConditionFromList(status.Conditions, conditionType)
}

// getStorageNodeConditionFromList returns the index and the condition based
// on the type from the given list of node conditions
func getStorageNodeConditionFromList(
	conditions []corev1alpha1.NodeCondition,
	conditionType corev1alpha1.NodeConditionType,
) (int, *corev1alpha1.NodeCondition) {
	if conditions == nil {
		return -1, nil
	}
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return i, &conditions[i]
		}
	}
	return -1, nil
}
