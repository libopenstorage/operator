/*
Copyright 2015 The Kubernetes Authors.
Modifications Copyright 2019 The Libopenstorage Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusteroperation

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/portworx/sched-ops/k8s"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// ControllerName is the name of the controller
	ControllerName      = "clusteroperation-controller"
	validateCRDInterval = 5 * time.Second
	validateCRDTimeout  = 1 * time.Minute
)

// Controller reconciles a ClusterOperation object
type Controller struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
}

// RegisterCRD is registering ClusterOperation CRD
func (c *Controller) RegisterCRD() error {
	return c.createCRD()
}

// Init is a controller initializer
func (c *Controller) Init(mgr manager.Manager) error {

	c.client = mgr.GetClient()

	ctrl, err := controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	// Create a source for watching ClusterOperation events.
	src := &source.Kind{Type: &corev1alpha1.ClusterOperation{}}
	// Create a handler for handling events from ClusterOperation resource.
	h := &handler.EnqueueRequestForObject{}
	pred := predicate.Funcs{
		// delete event is intentionally ignored
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}
	// Watch for ClusterOperation events.
	return ctrl.Watch(src, h, pred)
}

// Reconcile reconciles create and update events for ClusterOperation object
func (c *Controller) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	//TODO: A stub. Handle Create and Update events here
	err := c.updateClusterOperationStatus(&corev1alpha1.ClusterOperation{})
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (c *Controller) updateClusterOperationStatus(instance *corev1alpha1.ClusterOperation) error {
	//TODO: get object, update status
	return c.client.Status().Update(context.TODO(), instance)
}

// createCRD creates the CRD for ClusterOperation object
func (c *Controller) createCRD() error {

	crdName := fmt.Sprintf("%s.%s", corev1alpha1.ClusterOperationResourcePlural, corev1alpha1.SchemeGroupVersion.Group)

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: meta_v1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   corev1alpha1.SchemeGroupVersion.Group,
			Version: corev1alpha1.SchemeGroupVersion.Version,
			Scope:   apiextensionsv1beta1.NamespaceScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Singular:   corev1alpha1.ClusterOperationResourceName,
				Plural:     corev1alpha1.ClusterOperationResourcePlural,
				Kind:       reflect.TypeOf(corev1alpha1.ClusterOperation{}).Name(),
				ShortNames: []string{corev1alpha1.ClusterOperationShortName},
			},
			Subresources: &apiextensionsv1beta1.CustomResourceSubresources{
				Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
			},
		},
	}
	err := k8s.Instance().RegisterCRD(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	err = k8s.Instance().ValidateCRD(k8s.CustomResource{
		Name:   crd.ObjectMeta.Name,
		Plural: crd.Spec.Names.Plural,
		Group:  crd.Spec.Group,
		Kind:   crd.Spec.Names.Kind,
	}, validateCRDTimeout, validateCRDInterval)
	return err
}
