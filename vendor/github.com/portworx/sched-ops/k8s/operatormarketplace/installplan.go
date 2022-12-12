package operatormarketplace

import (
	"context"

	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InstallPlanOps is an interface to perfrom k8s InstallPlan operations
type InstallPlanOps interface {
	// CreateInstallPlan creates the given InstallPlan
	CreateInstallPlan(*v1alpha1.InstallPlan) (*v1alpha1.InstallPlan, error)
	// UpdateInstallPlan updates the given InstallPlan
	UpdateInstallPlan(*v1alpha1.InstallPlan) (*v1alpha1.InstallPlan, error)
	// GetInstallPlan gets the InstallPlan with given name and namespace
	GetInstallPlan(name, namespace string) (*v1alpha1.InstallPlan, error)
	// ListInstallPlans lists all InstallPlans in given namespace
	ListInstallPlans(namespace string) (*v1alpha1.InstallPlanList, error)
	// DeleteInstallPlan deletes the InstallPlan with given name and namespace
	DeleteInstallPlan(name, namespace string) error
}

// CreateInstallPlan creates the given InstallPlan
func (c *Client) CreateInstallPlan(installPlan *v1alpha1.InstallPlan) (*v1alpha1.InstallPlan, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Create(context.TODO(), installPlan); err != nil {
		return nil, err
	}
	return installPlan, nil
}

// UpdateInstallPlan updates the given InstallPlan
func (c *Client) UpdateInstallPlan(installPlan *v1alpha1.InstallPlan) (*v1alpha1.InstallPlan, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Update(context.TODO(), installPlan); err != nil {
		return nil, err
	}
	return installPlan, nil
}

// GetInstallPlan gets the InstallPlan with given name and namespace
func (c *Client) GetInstallPlan(name, namespace string) (*v1alpha1.InstallPlan, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	installPlan := &v1alpha1.InstallPlan{}
	if err := c.crClient.Get(
		context.TODO(),
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		installPlan,
	); err != nil {
		return nil, err
	}
	return installPlan, nil
}

// ListInstallPlans lists all InstallPlans in the given namespace
func (c *Client) ListInstallPlans(namespace string) (*v1alpha1.InstallPlanList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	installPlanList := &v1alpha1.InstallPlanList{}
	if err := c.crClient.List(
		context.TODO(),
		installPlanList,
		&client.ListOptions{
			Namespace: namespace,
		},
	); err != nil {
		return installPlanList, err
	}
	return installPlanList, nil
}

// DeleteInstallPlan deletes the InstallPlan with given name and namespace
func (c *Client) DeleteInstallPlan(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	installPlan := &v1alpha1.InstallPlan{}
	installPlan.Name = name
	installPlan.Namespace = namespace

	if err := c.crClient.Delete(context.TODO(), installPlan); err != nil {
		return err
	}
	return nil
}
