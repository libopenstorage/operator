package core

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/portworx/sched-ops/task"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// NodeOps is an interface to perform k8s node operations
type NodeOps interface {
	// CreateNode creates the given node
	CreateNode(n *corev1.Node) (*corev1.Node, error)
	// UpdateNode updates the given node
	UpdateNode(n *corev1.Node) (*corev1.Node, error)
	// GetNodes talks to the k8s api server and gets the nodes in the cluster
	GetNodes() (*corev1.NodeList, error)
	// GetNodeByName returns the k8s node given it's name
	GetNodeByName(string) (*corev1.Node, error)
	// SearchNodeByAddresses searches corresponding k8s node match any of the given address
	SearchNodeByAddresses(addresses []string) (*corev1.Node, error)
	// FindMyNode finds LOCAL Node in Kubernetes cluster
	FindMyNode() (*corev1.Node, error)
	// IsNodeReady checks if node with given name is ready. Returns nil is ready.
	IsNodeReady(string) error
	// IsNodeMaster returns true if given node is a kubernetes master node
	IsNodeMaster(corev1.Node) bool
	// GetLabelsOnNode gets all the labels on the given node
	GetLabelsOnNode(string) (map[string]string, error)
	// AddLabelOnNode adds a label key=value on the given node
	AddLabelOnNode(string, string, string) error
	// RemoveLabelOnNode removes the label with key on given node
	RemoveLabelOnNode(string, string) error
	// WatchNode sets up a watcher that listens for the changes on input node.Incase of input node as nil, It will watch on all the nodes
	WatchNode(node *corev1.Node, fn WatchFunc) error
	// CordonNode cordons the given node
	CordonNode(nodeName string, timeout, retryInterval time.Duration) error
	// UnCordonNode uncordons the given node
	UnCordonNode(nodeName string, timeout, retryInterval time.Duration) error
	// DrainPodsFromNode drains given pods from given node. If timeout is set to
	// a non-zero value, it waits for timeout duration for each pod to get deleted
	DrainPodsFromNode(nodeName string, pods []corev1.Pod, timeout, retryInterval time.Duration) error
	// DeleteNode deletes the given node
	DeleteNode(name string) error
	// GetWindowsNodes talks to the k8s api server and returns the Windows Nodes in the cluster
	GetWindowsNodes() (*corev1.NodeList, error)
	// GetLinuxNodes talks to the k8s api server and returns the Linux Nodes in the cluster
	GetLinuxNodes() (*corev1.NodeList, error)
	// GetReadyWindowsNodes talks to the k8s api server and returns the Windows Nodes in the cluster in Ready state
	GetReadyWindowsNodes() (*corev1.NodeList, error)
	// GetReadyLinuxNodes talks to the k8s api server and returns the Linux Nodes in the cluster
	GetReadyLinuxNodes() (*corev1.NodeList, error)
	// GetNodesUsingVolume returns the nodes using a PV
	GetNodesUsingVolume(pvName string, readyOnly bool) (*corev1.NodeList, error)
}

// CreateNode creates the given node
func (c *Client) CreateNode(n *corev1.Node) (*corev1.Node, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	return c.kubernetes.CoreV1().Nodes().Create(context.TODO(), n, metav1.CreateOptions{})
}

// DeleteNode deletes a node
func (c *Client) DeleteNode(name string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	err := c.kubernetes.CoreV1().Nodes().Delete(context.TODO(), name, metav1.DeleteOptions{})
	return err
}

// UpdateNode updates the given node
func (c *Client) UpdateNode(n *corev1.Node) (*corev1.Node, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	return c.kubernetes.CoreV1().Nodes().Update(context.TODO(), n, metav1.UpdateOptions{})
}

// GetNodes talks to the k8s api server and gets the nodes in the cluster
func (c *Client) GetNodes() (*corev1.NodeList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	nodes, err := c.kubernetes.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// GetNodeByName returns the k8s node given it's name
func (c *Client) GetNodeByName(name string) (*corev1.Node, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	node, err := c.kubernetes.CoreV1().Nodes().Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return node, nil
}

func (c *Client) checkReadyStatus(node *corev1.Node, name string) error {
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case corev1.NodeConditionType(corev1.NodeReady):
			if condition.Status != corev1.ConditionStatus(corev1.ConditionTrue) {
				return fmt.Errorf("node: %v is not ready as condition: %v (%v) is %v. Reason: %v",
					name, condition.Type, condition.Message, condition.Status, condition.Reason)
			}
		case corev1.NodeConditionType(corev1.NodeMemoryPressure),
			corev1.NodeConditionType(corev1.NodeDiskPressure),
			corev1.NodeConditionType(corev1.NodeNetworkUnavailable):
			// only checks if condition is true, ignoring condition Unknown
			if condition.Status == corev1.ConditionStatus(corev1.ConditionTrue) {
				return fmt.Errorf("node: %v is not ready as condition: %v (%v) is %v. Reason: %v",
					name, condition.Type, condition.Message, condition.Status, condition.Reason)
			}
		}
	}

	return nil
}

// IsNodeReady checks if node with given name is ready. Returns nil is ready.
func (c *Client) IsNodeReady(name string) error {
	node, err := c.GetNodeByName(name)
	if err != nil {
		return err
	}
	return c.checkReadyStatus(node, name)
}

// IsNodeMaster returns true if given node is a kubernetes master node
func (c *Client) IsNodeMaster(node corev1.Node) bool {
	// for newer k8s these fields exist but they are empty
	_, hasMasterLabel := node.Labels[masterLabelKey]
	_, hasControlPlaneLabel := node.Labels[controlplaneLabelKey]
	_, hasControlDashPlaneLabel := node.Labels[controlDashPlaneLabelKey]
	if hasMasterLabel || hasControlPlaneLabel || hasControlDashPlaneLabel {
		return true
	}
	return false
}

// GetLabelsOnNode gets all the labels on the given node
func (c *Client) GetLabelsOnNode(name string) (map[string]string, error) {
	node, err := c.GetNodeByName(name)
	if err != nil {
		return nil, err
	}

	return node.Labels, nil
}

// SearchNodeByAddresses searches the node based on the IP addresses, then it falls back to a
// search by hostname, and finally by the labels
func (c *Client) SearchNodeByAddresses(addresses []string) (*corev1.Node, error) {
	nodes, err := c.GetNodes()
	if err != nil {
		return nil, err
	}

	// sweep #1 - locating based on IP address
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			switch addr.Type {
			case corev1.NodeExternalIP:
				fallthrough
			case corev1.NodeInternalIP:
				for _, ip := range addresses {
					if addr.Address == ip {
						return &node, nil
					}
				}
			}
		}
	}

	// sweep #2 - locating based on Hostname
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			switch addr.Type {
			case corev1.NodeHostName:
				for _, ip := range addresses {
					if addr.Address == ip {
						return &node, nil
					}
				}
			}
		}
	}

	// sweep #3 - locating based on labels
	for _, node := range nodes.Items {
		if hn, has := node.GetLabels()[corev1.LabelHostname]; has {
			for _, ip := range addresses {
				if hn == ip {
					return &node, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to find k8s node for given addresses: %v", addresses)
}

// FindMyNode finds LOCAL Node in Kubernetes cluster.
func (c *Client) FindMyNode() (*corev1.Node, error) {
	ipList, err := getLocalIPList(true)
	if err != nil {
		return nil, fmt.Errorf("Could not find my IPs/Hostname: %s", err)
	}
	return c.SearchNodeByAddresses(ipList)
}

// AddLabelOnNode adds a label key=value on the given node
func (c *Client) AddLabelOnNode(name, key, value string) error {
	var err error
	if err := c.initClient(); err != nil {
		return err
	}

	retryCnt := 0
	for retryCnt < labelUpdateMaxRetries {
		retryCnt++

		node, err := c.kubernetes.CoreV1().Nodes().Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if val, present := node.Labels[key]; present && val == value {
			return nil
		}

		node.Labels[key] = value
		if _, err = c.kubernetes.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err == nil {
			return nil
		}
	}

	return err
}

// RemoveLabelOnNode removes the label with key on given node
func (c *Client) RemoveLabelOnNode(name, key string) error {
	var err error
	if err := c.initClient(); err != nil {
		return err
	}

	retryCnt := 0
	for retryCnt < labelUpdateMaxRetries {
		retryCnt++

		node, err := c.kubernetes.CoreV1().Nodes().Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if _, present := node.Labels[key]; present {
			delete(node.Labels, key)
			if _, err = c.kubernetes.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err == nil {
				return nil
			}
		}
	}

	return err
}

// WatchNode sets up a watcher that listens for the changes on input node and will watch all the nodes when input node is nil.
func (c *Client) WatchNode(node *corev1.Node, watchNodeFn WatchFunc) error {
	if err := c.initClient(); err != nil {
		return err
	}
	listOptions := metav1.ListOptions{
		Watch: true,
	}
	if node != nil {
		listOptions.FieldSelector = fields.OneTermEqualSelector("metadata.name", node.Name).String()
	} else {
		fmt.Printf("Watching all nodes")
	}
	watchInterface, err := c.kubernetes.CoreV1().Nodes().Watch(context.TODO(), listOptions)
	if err != nil {
		return err
	}

	// fire off watch function
	go c.handleWatch(watchInterface, node, "", watchNodeFn, listOptions)
	return nil
}

// CordonNode cordons the given node
func (c *Client) CordonNode(nodeName string, timeout, retryInterval time.Duration) error {
	t := func() (interface{}, bool, error) {
		if err := c.initClient(); err != nil {
			return nil, true, err
		}

		n, err := c.GetNodeByName(nodeName)
		if err != nil {
			return nil, true, err
		}

		nCopy := n.DeepCopy()
		nCopy.Spec.Unschedulable = true
		n, err = c.kubernetes.CoreV1().Nodes().Update(context.TODO(), nCopy, metav1.UpdateOptions{})
		if err != nil {
			return nil, true, err
		}

		return nil, false, nil

	}

	if _, err := task.DoRetryWithTimeout(t, timeout, retryInterval); err != nil {
		return err
	}

	return nil
}

// UnCordonNode uncordons the given node
func (c *Client) UnCordonNode(nodeName string, timeout, retryInterval time.Duration) error {
	t := func() (interface{}, bool, error) {
		if err := c.initClient(); err != nil {
			return nil, true, err
		}

		n, err := c.GetNodeByName(nodeName)
		if err != nil {
			return nil, true, err
		}

		nCopy := n.DeepCopy()
		nCopy.Spec.Unschedulable = false
		n, err = c.kubernetes.CoreV1().Nodes().Update(context.TODO(), nCopy, metav1.UpdateOptions{})
		if err != nil {
			return nil, true, err
		}

		return nil, false, nil

	}

	if _, err := task.DoRetryWithTimeout(t, timeout, retryInterval); err != nil {
		return err
	}

	return nil
}

// DrainPodsFromNode drains given pods from given node. If timeout is set to
// a non-zero value, it waits for timeout duration for each pod to get deleted
func (c *Client) DrainPodsFromNode(nodeName string, pods []corev1.Pod, timeout time.Duration, retryInterval time.Duration) error {
	err := c.CordonNode(nodeName, timeout, retryInterval)
	if err != nil {
		return err
	}

	err = c.DeletePods(pods, false)
	if err != nil {
		e := c.UnCordonNode(nodeName, timeout, retryInterval) // rollback cordon
		if e != nil {
			log.Printf("failed to uncordon node: %s", nodeName)
		}
		return err
	}

	if timeout > 0 {
		for _, p := range pods {
			err = c.WaitForPodDeletion(p.UID, p.Namespace, timeout)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Client) getTaggedNodes(partialLabelName, partialLabelValue string, readyOnlyNodes bool) (*corev1.NodeList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	allNodes, err := c.kubernetes.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var retNodes corev1.NodeList
	for _, n := range allNodes.Items {
		if readyOnlyNodes {
			readyErr := c.checkReadyStatus(&n, n.Name)
			if readyErr != nil {
				continue
			}
		}
		if partialLabelName != "" && partialLabelValue != "" {
			for k, v := range n.GetLabels() {
				if strings.Contains(k, partialLabelName) {
					if strings.EqualFold(v, partialLabelValue) {
						retNodes.Items = append(retNodes.Items, n)
					}
					break // break from label, os label found
				}
			}
		} else {
			retNodes.Items = append(retNodes.Items, n)
		}
	}
	return &retNodes, nil
}

// GetLinuxNodes talks to the k8s api server and returns the linux nodes in the cluster
func (c *Client) GetLinuxNodes() (*corev1.NodeList, error) {
	return c.getTaggedNodes("kubernetes.io/os", "linux", false)
}

// GetWindowsNodes talks to the k8s api server to get all nodes and filter on labels to get Windows nodes
func (c *Client) GetWindowsNodes() (*corev1.NodeList, error) {
	return c.getTaggedNodes("kubernetes.io/os", "windows", false)
}

// GetReadyLinuxNodes talks to the k8s api server to get all nodes and filters linux nodes that are Ready.
func (c *Client) GetReadyLinuxNodes() (*corev1.NodeList, error) {
	return c.getTaggedNodes("kubernetes.io/os", "linux", true)
}

// GetReadyWindowsNodes talks to the k8s api server to get all nodes and filter on labels to get Windows nodes that are Ready.
func (c *Client) GetReadyWindowsNodes() (*corev1.NodeList, error) {
	return c.getTaggedNodes("kubernetes.io/os", "windows", true)
}

// GetNodesUsingVolume Returns the list of nodes using a Pv.
func (c *Client) GetNodesUsingVolume(pvName string, readyNodesOnly bool) (*corev1.NodeList, error) {
	allNodes, err := c.getTaggedNodes("", "", readyNodesOnly)
	if err != nil {
		return nil, err
	}
	var pvNodes corev1.NodeList
	for _, n := range allNodes.Items {
		pods, err := c.GetPodsUsingPVByNodeName(pvName, n.Name)
		if err == nil && (len(pods) > 0) {
			pvNodes.Items = append(pvNodes.Items, n)
		}
	}
	return &pvNodes, nil
}
