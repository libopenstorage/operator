// +build integrationtest

package integrationtest

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"github.com/portworx/sched-ops/task"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/algorithm/predicates"
	schedulernodeinfo "k8s.io/kubernetes/pkg/scheduler/nodeinfo"
)

const (
	specDir = "./specs"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func createStorageClusterFromSpec(filename string) (*corev1alpha1.StorageCluster, error) {
	filepath := path.Join(specDir, filename)
	scheme := runtime.NewScheme()
	cluster := &corev1alpha1.StorageCluster{}
	if err := k8sutil.ParseObjectFromFile(filepath, scheme, cluster); err != nil {
		return nil, err
	}
	if err := createStorageCluster(cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

func createStorageCluster(cluster *corev1alpha1.StorageCluster) error {
	_, err := k8s.Instance().CreateStorageCluster(cluster)
	return err
}

func uninstallStorageCluster(cluster *corev1alpha1.StorageCluster) error {
	var err error
	cluster, err = k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if cluster.Spec.DeleteStrategy == nil ||
		(cluster.Spec.DeleteStrategy.Type != corev1alpha1.UninstallAndWipeStorageClusterStrategyType &&
			cluster.Spec.DeleteStrategy.Type != corev1alpha1.UninstallStorageClusterStrategyType) {
		cluster.Spec.DeleteStrategy = &corev1alpha1.StorageClusterDeleteStrategy{
			Type: corev1alpha1.UninstallAndWipeStorageClusterStrategyType,
		}
		if _, err = k8s.Instance().UpdateStorageCluster(cluster); err != nil {
			return err
		}
	}

	return k8s.Instance().DeleteStorageCluster(cluster.Name, cluster.Namespace)
}

func validateStorageCluster(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	// Validate expected portworx pods are running and no more, no less
	return validateStorageClusterPods(cluster, timeout, interval)

	// TODO: Validate expected number of portworx nodes are online (using sdk)
	// TODO: Validate portworx is started with correct params. Check individual options
}

func validateStorageClusterComponents(cluster *corev1alpha1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}

func validateUninstallStorageCluster(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	t := func() (interface{}, bool, error) {
		cluster, err := k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			if errors.IsNotFound(err) {
				return "", false, nil
			}
			return "", true, err
		}

		pods, err := k8s.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil && err != k8s.ErrPodsNotFound {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if pods != nil && len(pods) > 0 {
			return "", true, fmt.Errorf("%v pods are still present", len(pods))
		}

		return "", true, fmt.Errorf("pods are deleted, but StorageCluster %v/%v still present",
			cluster.Namespace, cluster.Name)
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}
	return nil
}

func validateStorageClusterPods(
	cluster *corev1alpha1.StorageCluster,
	timeout, interval time.Duration,
) error {
	expectedPodCount, err := expectedPods(cluster)
	if err != nil {
		return err
	}

	t := func() (interface{}, bool, error) {
		cluster, err := k8s.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return "", true, err
		}

		pods, err := k8s.Instance().GetPodsByOwner(cluster.UID, cluster.Namespace)
		if err != nil || pods == nil {
			return "", true, fmt.Errorf("failed to get pods for StorageCluster %s/%s. Err: %v",
				cluster.Namespace, cluster.Name, err)
		}

		if len(pods) != expectedPodCount {
			return "", true, fmt.Errorf("expected pods: %v. actual pods: %v", expectedPodCount, len(pods))
		}

		for _, pod := range pods {
			if !k8s.Instance().IsPodReady(pod) {
				return "", true, fmt.Errorf("pod %v/%v is not yet ready", pod.Namespace, pod.Name)
			}
		}

		return "", false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, timeout, interval); err != nil {
		return err
	}
	return nil
}

func expectedPods(cluster *corev1alpha1.StorageCluster) (int, error) {
	nodeList, err := k8s.Instance().GetNodes()
	if err != nil {
		return 0, err
	}

	dummyPod := &v1.Pod{}
	if cluster.Spec.Placement != nil && cluster.Spec.Placement.NodeAffinity != nil {
		dummyPod.Spec.Affinity = &v1.Affinity{
			NodeAffinity: cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	podCount := 0
	for _, node := range nodeList.Items {
		if k8s.Instance().IsNodeMaster(node) {
			continue
		}
		nodeInfo := schedulernodeinfo.NewNodeInfo()
		nodeInfo.SetNode(&node)
		if ok, _, _ := predicates.PodMatchNodeSelector(dummyPod, nil, nodeInfo); ok {
			podCount++
		}
	}

	return podCount, nil
}
