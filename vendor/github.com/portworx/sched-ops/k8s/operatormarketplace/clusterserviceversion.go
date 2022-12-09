package operatormarketplace

import (
	"context"

	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClusterServiceVersionOps is an interface to perfrom k8s ClusterServiceVersion operations
type ClusterServiceVersionOps interface {
	// CreateClusterServiceVersion creates the given ClusterServiceVersion
	CreateClusterServiceVersion(*v1alpha1.ClusterServiceVersion) (*v1alpha1.ClusterServiceVersion, error)
	// UpdateClusterServiceVersion updates the given ClusterServiceVersion
	UpdateClusterServiceVersion(*v1alpha1.ClusterServiceVersion) (*v1alpha1.ClusterServiceVersion, error)
	// GetClusterServiceVersion gets the ClusterServiceVersion with given name and namespace
	GetClusterServiceVersion(name, namespace string) (*v1alpha1.ClusterServiceVersion, error)
	// ListClusterServiceVersions lists all ClusterServiceVersions in given namespace
	ListClusterServiceVersions(namespace string) (*v1alpha1.ClusterServiceVersionList, error)
	// DeleteClusterServiceVersion deletes the ClusterServiceVersion with given name and namespace
	DeleteClusterServiceVersion(name, namespace string) error
}

// CreateClusterServiceVersion creates the given ClusterServiceVersion
func (c *Client) CreateClusterServiceVersion(clusterServiceVersion *v1alpha1.ClusterServiceVersion) (*v1alpha1.ClusterServiceVersion, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Create(context.TODO(), clusterServiceVersion); err != nil {
		return nil, err
	}
	return clusterServiceVersion, nil
}

// UpdateClusterServiceVersion updates the given ClusterServiceVersion
func (c *Client) UpdateClusterServiceVersion(clusterServiceVersion *v1alpha1.ClusterServiceVersion) (*v1alpha1.ClusterServiceVersion, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Update(context.TODO(), clusterServiceVersion); err != nil {
		return nil, err
	}
	return clusterServiceVersion, nil
}

// GetClusterServiceVersion gets the ClusterServiceVersion with given name and namespace
func (c *Client) GetClusterServiceVersion(name, namespace string) (*v1alpha1.ClusterServiceVersion, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	clusterServiceVersion := &v1alpha1.ClusterServiceVersion{}
	if err := c.crClient.Get(
		context.TODO(),
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		clusterServiceVersion,
	); err != nil {
		return nil, err
	}
	return clusterServiceVersion, nil
}

// ListClusterServiceVersions lists all ClusterServiceVersions in the given namespace
func (c *Client) ListClusterServiceVersions(namespace string) (*v1alpha1.ClusterServiceVersionList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	clusterServiceVersionList := &v1alpha1.ClusterServiceVersionList{}
	if err := c.crClient.List(
		context.TODO(),
		clusterServiceVersionList,
		&client.ListOptions{
			Namespace: namespace,
		},
	); err != nil {
		return clusterServiceVersionList, err
	}
	return clusterServiceVersionList, nil
}

// DeleteClusterServiceVersion deletes the ClusterServiceVersion with given name and namespace
func (c *Client) DeleteClusterServiceVersion(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	clusterServiceVersion := &v1alpha1.ClusterServiceVersion{}
	clusterServiceVersion.Name = name
	clusterServiceVersion.Namespace = namespace

	if err := c.crClient.Delete(context.TODO(), clusterServiceVersion); err != nil {
		return err
	}
	return nil
}
