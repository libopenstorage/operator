package operatormarketplace

import (
	"context"

	v1 "github.com/operator-framework/api/pkg/operators/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OperatorGroupOps is an interface to perfrom k8s OperatorGroup operations
type OperatorGroupOps interface {
	// CreateOperatorGroup creates the given OperatorGroup
	CreateOperatorGroup(*v1.OperatorGroup) (*v1.OperatorGroup, error)
	// UpdateOperatorGroup updates the given OperatorGroup
	UpdateOperatorGroup(*v1.OperatorGroup) (*v1.OperatorGroup, error)
	// GetOperatorGroup gets the OperatorGroup with given name and namespace
	GetOperatorGroup(name, namespace string) (*v1.OperatorGroup, error)
	// ListOperatorGroups lists all OperatorGroups in given namespace
	ListOperatorGroups(namespace string) (*v1.OperatorGroupList, error)
	// DeleteOperatorGroup deletes the OperatorGroup with given name and namespace
	DeleteOperatorGroup(name, namespace string) error
}

// CreateOperatorGroup creates the given OperatorGroup
func (c *Client) CreateOperatorGroup(operatorGroup *v1.OperatorGroup) (*v1.OperatorGroup, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Create(context.TODO(), operatorGroup); err != nil {
		return nil, err
	}
	return operatorGroup, nil
}

// UpdateOperatorGroup updates the given OperatorGroup
func (c *Client) UpdateOperatorGroup(operatorGroup *v1.OperatorGroup) (*v1.OperatorGroup, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Update(context.TODO(), operatorGroup); err != nil {
		return nil, err
	}
	return operatorGroup, nil
}

// GetOperatorGroup gets the OperatorGroup with given name and namespace
func (c *Client) GetOperatorGroup(name, namespace string) (*v1.OperatorGroup, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	operatorGroup := &v1.OperatorGroup{}
	if err := c.crClient.Get(
		context.TODO(),
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		operatorGroup,
	); err != nil {
		return nil, err
	}
	return operatorGroup, nil
}

// ListOperatorGroups lists all OperatorGroups in the given namespace
func (c *Client) ListOperatorGroups(namespace string) (*v1.OperatorGroupList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	operatorGroupList := &v1.OperatorGroupList{}
	if err := c.crClient.List(
		context.TODO(),
		operatorGroupList,
		&client.ListOptions{
			Namespace: namespace,
		},
	); err != nil {
		return operatorGroupList, err
	}
	return operatorGroupList, nil
}

// DeleteOperatorGroup deletes the OperatorGroup with given name and namespace
func (c *Client) DeleteOperatorGroup(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	operatorGroup := &v1.OperatorGroup{}
	operatorGroup.Name = name
	operatorGroup.Namespace = namespace

	if err := c.crClient.Delete(context.TODO(), operatorGroup); err != nil {
		return err
	}
	return nil
}
