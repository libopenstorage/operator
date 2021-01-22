// +build integrationtest

package integrationtest

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	specDir = "./specs"

	defaultWaitForStorageClusterToBeReadyRetryInterval = 10 * time.Second
	defaultWaitForStorageClusterToBeReadyTimeout       = 600 * time.Second
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func createStorageClusterFromSpec(filename string) (*corev1.StorageCluster, error) {
	filepath := path.Join(specDir, filename)
	scheme := runtime.NewScheme()
	cluster := &corev1.StorageCluster{}
	if err := k8sutil.ParseObjectFromFile(filepath, scheme, cluster); err != nil {
		return nil, err
	}
	if err := createStorageCluster(cluster); err != nil {
		return nil, err
	}
	return cluster, nil
}

func createStorageCluster(cluster *corev1.StorageCluster) error {
	_, err := operator.Instance().CreateStorageCluster(cluster)
	return err
}

func validateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}

// TODO: This will eventually be replaced by ValidateStorageCluster() when its fully implemented
func waitForStorageClusterToBeReady(cluster *corev1.StorageCluster) error {
	// Get StorageCluster in retry loop
	t := func() (interface{}, bool, error) {
		cluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
		if err != nil {
			return nil, true, fmt.Errorf("Failed to get StorageCluster %s in %s, Err: %v", cluster.Name, cluster.Namespace, err)
		}

		if cluster.Status.Phase != "Online" {
			return nil, true, fmt.Errorf("Cluster is in State: %s", cluster.Status.Phase)
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, defaultWaitForStorageClusterToBeReadyTimeout, defaultWaitForStorageClusterToBeReadyRetryInterval); err != nil {
		return fmt.Errorf("Failed to wait for StorageCluster to be ready, Err: %v", err)
	}
	return nil
}
