package k8s

import (
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StorageClusterOps is an interface to perfrom k8s StorageCluster operations
type StorageClusterOps interface {
	// CreateStorageCluster creates the given StorageCluster
	CreateStorageCluster(*corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error)
	// UpdateStorageCluster updates the given StorageCluster
	UpdateStorageCluster(*corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error)
	// GetStorageCluster gets the StorageCluster with given name and namespace
	GetStorageCluster(string, string) (*corev1alpha1.StorageCluster, error)
	// ListStorageClusters lists all the StorageClusters
	ListStorageClusters(string) (*corev1alpha1.StorageClusterList, error)
	// DeleteStorageCluster deletes the given StorageCluster
	DeleteStorageCluster(string, string) error
	// UpdateStorageClusterStatus update the status of given StorageCluster
	UpdateStorageClusterStatus(*corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error)
}

// StorageCluster APIs - BEGIN

func (k *k8sOps) CreateStorageCluster(cluster *corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	ns := cluster.Namespace
	if len(ns) == 0 {
		ns = v1.NamespaceDefault
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(ns).Create(cluster)
}

func (k *k8sOps) UpdateStorageCluster(cluster *corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(cluster.Namespace).Update(cluster)
}

func (k *k8sOps) GetStorageCluster(name, namespace string) (*corev1alpha1.StorageCluster, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(namespace).Get(name, meta_v1.GetOptions{})
}

func (k *k8sOps) ListStorageClusters(namespace string) (*corev1alpha1.StorageClusterList, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(namespace).List(meta_v1.ListOptions{})
}

func (k *k8sOps) DeleteStorageCluster(name, namespace string) error {
	if err := k.initK8sClient(); err != nil {
		return err
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(namespace).Delete(name, &meta_v1.DeleteOptions{
		PropagationPolicy: &deleteForegroundPolicy,
	})
}

func (k *k8sOps) UpdateStorageClusterStatus(cluster *corev1alpha1.StorageCluster) (*corev1alpha1.StorageCluster, error) {
	if err := k.initK8sClient(); err != nil {
		return nil, err
	}

	return k.ostClient.CoreV1alpha1().StorageClusters(cluster.Namespace).UpdateStatus(cluster)
}

// StorageCluster APIs - END
