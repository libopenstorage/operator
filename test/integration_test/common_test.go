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
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	specDir = "./specs"

	defaultWaitForStorageClusterToBeReadyRetryInterval = 10 * time.Second
	defaultWaitForStorageClusterToBeReadyTimeout       = 600 * time.Second
)

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		logrus.Errorf("Setup failed with error: %v", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func setup() error {
	if err := setKubeconfig(""); err != nil {
		return err
	}

	return nil
}

func setKubeconfig(kubeConfig string) error {
	var config *rest.Config
	var err error

	if kubeConfig == "" {
		config = nil
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			return err
		}
	}

	k8sOps := core.Instance()
	k8sOps.SetConfig(config)

	operatorOps := operator.Instance()
	operatorOps.SetConfig(config)

	return nil
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
