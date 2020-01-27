// +build integrationtest

package integrationtest

import (
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/sched-ops/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	"path"
	"testing"
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

func validateStorageClusterComponents(cluster *corev1alpha1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}
