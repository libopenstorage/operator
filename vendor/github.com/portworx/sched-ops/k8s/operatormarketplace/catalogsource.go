package operatormarketplace

import (
	"context"

	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CatalogSourceOps is an interface to perfrom k8s CatalogSource operations
type CatalogSourceOps interface {
	// CreateCatalogSource creates the given CatalogSource
	CreateCatalogSource(*v1alpha1.CatalogSource) (*v1alpha1.CatalogSource, error)
	// UpdateCatalogSource updates the given CatalogSource
	UpdateCatalogSource(*v1alpha1.CatalogSource) (*v1alpha1.CatalogSource, error)
	// GetCatalogSource gets the CatalogSource with given name and namespace
	GetCatalogSource(name, namespace string) (*v1alpha1.CatalogSource, error)
	// ListCatalogSources lists all CatalogSources in given namespace
	ListCatalogSources(namespace string) (*v1alpha1.CatalogSourceList, error)
	// DeleteCatalogSource deletes the CatalogSource with given name and namespace
	DeleteCatalogSource(name, namespace string) error
}

// CreateCatalogSource creates the given CatalogSource
func (c *Client) CreateCatalogSource(catalogSource *v1alpha1.CatalogSource) (*v1alpha1.CatalogSource, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Create(context.TODO(), catalogSource); err != nil {
		return nil, err
	}
	return catalogSource, nil
}

// UpdateCatalogSource updates the given CatalogSource
func (c *Client) UpdateCatalogSource(catalogSource *v1alpha1.CatalogSource) (*v1alpha1.CatalogSource, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Update(context.TODO(), catalogSource); err != nil {
		return nil, err
	}
	return catalogSource, nil
}

// GetCatalogSource gets the CatalogSource with given name and namespace
func (c *Client) GetCatalogSource(name, namespace string) (*v1alpha1.CatalogSource, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	catalogSource := &v1alpha1.CatalogSource{}
	if err := c.crClient.Get(
		context.TODO(),
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		catalogSource,
	); err != nil {
		return nil, err
	}
	return catalogSource, nil
}

// ListCatalogSources lists all CatalogSources in the given namespace
func (c *Client) ListCatalogSources(namespace string) (*v1alpha1.CatalogSourceList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	catalogSourceList := &v1alpha1.CatalogSourceList{}
	if err := c.crClient.List(
		context.TODO(),
		catalogSourceList,
		&client.ListOptions{
			Namespace: namespace,
		},
	); err != nil {
		return catalogSourceList, err
	}
	return catalogSourceList, nil
}

// DeleteCatalogSource deletes the CatalogSource with given name and namespace
func (c *Client) DeleteCatalogSource(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	catalogSource := &v1alpha1.CatalogSource{}
	catalogSource.Name = name
	catalogSource.Namespace = namespace

	if err := c.crClient.Delete(context.TODO(), catalogSource); err != nil {
		return err
	}
	return nil
}
