package operatormarketplace

import (
	"context"

	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SubscriptionOps is an interface to perfrom k8s Subscription operations
type SubscriptionOps interface {
	// CreateSubscription creates the given Subscription
	CreateSubscription(*v1alpha1.Subscription) (*v1alpha1.Subscription, error)
	// UpdateSubscription updates the given Subscription
	UpdateSubscription(*v1alpha1.Subscription) (*v1alpha1.Subscription, error)
	// GetSubscription gets the Subscription with given name and namespace
	GetSubscription(name, namespace string) (*v1alpha1.Subscription, error)
	// ListSubscriptions lists all Subscriptions in given namespace
	ListSubscriptions(namespace string) (*v1alpha1.SubscriptionList, error)
	// DeleteSubscription deletes the Subscription with given name and namespace
	DeleteSubscription(name, namespace string) error
}

// CreateSubscription creates the given Subscription
func (c *Client) CreateSubscription(subscription *v1alpha1.Subscription) (*v1alpha1.Subscription, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Create(context.TODO(), subscription); err != nil {
		return nil, err
	}
	return subscription, nil
}

// UpdateSubscription updates the given Subscription
func (c *Client) UpdateSubscription(subscription *v1alpha1.Subscription) (*v1alpha1.Subscription, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	if err := c.crClient.Update(context.TODO(), subscription); err != nil {
		return nil, err
	}
	return subscription, nil
}

// GetSubscription gets the Subscription with given name and namespace
func (c *Client) GetSubscription(name, namespace string) (*v1alpha1.Subscription, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	subscription := &v1alpha1.Subscription{}
	if err := c.crClient.Get(
		context.TODO(),
		types.NamespacedName{
			Namespace: namespace,
			Name:      name,
		},
		subscription,
	); err != nil {
		return nil, err
	}
	return subscription, nil
}

// ListSubscriptions lists all Subscriptions in the given namespace
func (c *Client) ListSubscriptions(namespace string) (*v1alpha1.SubscriptionList, error) {
	if err := c.initClient(); err != nil {
		return nil, err
	}

	subscriptionList := &v1alpha1.SubscriptionList{}
	if err := c.crClient.List(
		context.TODO(),
		subscriptionList,
		&client.ListOptions{
			Namespace: namespace,
		},
	); err != nil {
		return subscriptionList, err
	}
	return subscriptionList, nil
}

// DeleteSubscription deletes the Subscription with given name and namespace
func (c *Client) DeleteSubscription(name, namespace string) error {
	if err := c.initClient(); err != nil {
		return err
	}

	subscription := &v1alpha1.Subscription{}
	subscription.Name = name
	subscription.Namespace = namespace

	if err := c.crClient.Delete(context.TODO(), subscription); err != nil {
		return err
	}
	return nil
}
