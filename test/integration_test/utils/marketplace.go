package utils

import (
	"fmt"
	"time"

	v1 "github.com/operator-framework/api/pkg/operators/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	opmpops "github.com/portworx/sched-ops/k8s/operatormarketplace"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
)

const (
	defaultOperatorRegistryImageName = "docker.io/portworx/px-operator-registry"
	defaultCatalogSourceName         = "test-catalog"
	defaultCatalogSourceNamespace    = "openshift-marketplace"
	defaultOperatorGroupName         = "test-operator-group"
	defaultSubscriptionName          = "test-portworx-certified"

	getInstallPlanListTimeout       = 1 * time.Minute
	getInstallPlanListRetryInterval = 2 * time.Second
)

// DeployAndValidatePxOperatorViaMarketplace deploys and validates PX Operator via Openshift Marketplace
func DeployAndValidatePxOperatorViaMarketplace() error {
	// Deploy CatalogSource
	opRegistryImage := fmt.Sprintf("%s:%s", defaultOperatorRegistryImageName, PxOperatorTag)
	testCatalogSource, err := DeployCatalogSource(defaultCatalogSourceName, defaultCatalogSourceNamespace, opRegistryImage)
	if err != nil {
		return err
	}

	// Deploy OperatorGroup
	_, err = DeployOperatorGroup(defaultOperatorGroupName, PxNamespace)
	if err != nil {
		return err
	}

	// Deploy Subscription
	testSubscription, err := DeploySubscription(defaultSubscriptionName, PxNamespace, PxOperatorTag, testCatalogSource)
	if err != nil {
		return err
	}

	// Approve InstallPlan
	if err := ApproveInstallPlan(PxNamespace, PxOperatorTag, testSubscription); err != nil {
		return err
	}

	// Validate PX Operator deployment
	if _, err := ValidatePxOperator(PxNamespace); err != nil {
		return err
	}

	logrus.Infof("Successfully deployed PX Operator in namespace [%s]", PxNamespace)
	return nil
}

// DeleteAndValidatePxOperatorViaMarketplace deletes and validates PX Operator and its resources via Openshift Marketplace
func DeleteAndValidatePxOperatorViaMarketplace() error {
	logrus.Infof("Delete PX Operator deployment in namespace [%s]", PxNamespace)

	// Get currentCSV from Subscription
	currentCSV, err := GetCurrentCSV(defaultSubscriptionName, PxNamespace)
	if err != nil {
		return fmt.Errorf("failed to get currentCSV, Err: %v", err)
	}

	// Delete ClusterVersion
	if err := DeleteClusterServiceVersion(currentCSV, PxNamespace); err != nil {
		return err
	}

	// Delete Subscription
	if err := DeleteSubscription(defaultSubscriptionName, PxNamespace); err != nil {
		return err
	}

	// Delete OperatorGroup
	if err := DeleteOperatorGroup(defaultOperatorGroupName, PxNamespace); err != nil {
		return err
	}

	// Delete CatalogSource
	if err := DeleteCatalogSource(defaultCatalogSourceName, defaultCatalogSourceNamespace); err != nil {
		return err
	}

	// Validate PX Operator is deleted
	if _, err := ValidatePxOperatorDeleted(PxNamespace); err != nil {
		return err
	}

	logrus.Infof("Successfully deleted PX Operator deployment in namespace [%s]", PxNamespace)
	return nil
}

// DeployCatalogSource creates CatalogSource resource
func DeployCatalogSource(name, namespace, registryImage string) (*v1alpha1.CatalogSource, error) {
	logrus.Infof("Create test CatalogSrouce [%s] in namespace [%s]", name, namespace)
	testCatalogSourceTemplate := &v1alpha1.CatalogSource{
		Spec: v1alpha1.CatalogSourceSpec{
			Image:      registryImage,
			SourceType: v1alpha1.SourceTypeGrpc,
		},
	}

	testCatalogSourceTemplate.Name = name
	testCatalogSourceTemplate.Namespace = namespace
	testCatalogSource, err := opmpops.Instance().CreateCatalogSource(testCatalogSourceTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to create CatalogSource [%s] in namespace [%s], Err: %v", testCatalogSourceTemplate.Name, testCatalogSourceTemplate.Namespace, err)
	}
	logrus.Infof("Successfully created test CatalogSrouce [%s] in namespace [%s]", testCatalogSource.Name, testCatalogSource.Namespace)
	return testCatalogSource, nil
}

// DeployOperatorGroup creates OperatorGroup resource
func DeployOperatorGroup(name, namespace string) (*v1.OperatorGroup, error) {
	logrus.Infof("Create test OperatorGroup [%s] in namespace [%s]", name, namespace)
	testOperatorGroupTemplate := &v1.OperatorGroup{
		Spec: v1.OperatorGroupSpec{
			TargetNamespaces: []string{namespace},
		},
	}
	testOperatorGroupTemplate.Name = name
	testOperatorGroupTemplate.Namespace = namespace
	testOperatorGroup, err := opmpops.Instance().CreateOperatorGroup(testOperatorGroupTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to create OperatorGroup [%s] in namespace [%s], Err: %v", testOperatorGroupTemplate.Name, testOperatorGroupTemplate.Namespace, err)
	}
	logrus.Infof("Successfully created test OperatorGroup [%s] in namespace [%s]", testOperatorGroup.Name, testOperatorGroup.Namespace)
	return testOperatorGroup, nil
}

// DeploySubscription creates Subscription resource
func DeploySubscription(name, namespace, opTag string, testCatalogSource *v1alpha1.CatalogSource) (*v1alpha1.Subscription, error) {
	logrus.Infof("Create test Subscription [%s] in namespace [%s]", name, namespace)
	testSubscriptionTemplate := &v1alpha1.Subscription{
		Spec: &v1alpha1.SubscriptionSpec{
			Channel:                "stable",
			InstallPlanApproval:    v1alpha1.ApprovalManual,
			Package:                "portworx-certified",
			CatalogSource:          testCatalogSource.Name,
			CatalogSourceNamespace: testCatalogSource.Namespace,
			StartingCSV:            fmt.Sprintf("portworx-operator.v%s", opTag),
		},
	}
	testSubscriptionTemplate.Name = name
	testSubscriptionTemplate.Namespace = namespace
	testSubscription, err := opmpops.Instance().CreateSubscription(testSubscriptionTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to create Subscription [%s] in namespace [%s], Err: %v", testSubscriptionTemplate.Name, testSubscriptionTemplate.Namespace, err)
	}
	logrus.Infof("Successfully created test Subscription [%s] in namespace [%s]", testSubscription.Name, testSubscription.Namespace)
	return testSubscription, nil
}

// ApproveInstallPlan finds correct install plan and approves it
func ApproveInstallPlan(namespace, opTag string, subscription *v1alpha1.Subscription) error {
	logrus.Infof("Looking for InstallPlan and setting Approved to true")
	t := func() (interface{}, bool, error) {
		instpl, err := opmpops.Instance().ListInstallPlans(namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get list of InstallPlans in namespace [%s], Err: %v", namespace, err)
		}

		if instpl != nil && len(instpl.Items) > 0 {
			logrus.Infof("Successfully got list of InstallPlans in namespace [%s]", namespace)
			for _, instp := range instpl.Items {
				if subscription.Name == instp.OwnerReferences[0].Name {
					logrus.Infof("found the correct InstallPlan [%s] in namespace [%s]", instp.Name, instp.Namespace)
					return instp, false, nil
				}
			}
			return nil, true, fmt.Errorf("failed to find the correct InstallPlan with OwnerReference for Subscription [%s]", subscription.Name)
		}
		return nil, true, fmt.Errorf("failed to find any InstallPlans in namespace [%s]", namespace)
	}

	instPlan, err := task.DoRetryWithTimeout(t, getInstallPlanListTimeout, getInstallPlanListRetryInterval)
	if err != nil {
		return err
	}

	installPlan := instPlan.(v1alpha1.InstallPlan)
	logrus.Infof("Update Approved to true for InstallPlan [%s]", installPlan.Name)
	installPlan.Spec.Approved = true
	_, err = opmpops.Instance().UpdateInstallPlan(&installPlan)
	if err != nil {
		return fmt.Errorf("failed to update InstallPlan [%s]", installPlan.Name)
	}
	logrus.Infof("Successfully Approved InstallPlan [%s]", installPlan.Name)
	return nil
}

// DeleteClusterServiceVersion deletes ClusterServiceVersion resource
func DeleteClusterServiceVersion(name, namespace string) error {
	logrus.Infof("Deleting ClusterServiceVersion [%s] in namespace [%s]", name, namespace)
	if err := opmpops.Instance().DeleteClusterServiceVersion(name, namespace); err != nil {
		return fmt.Errorf("failed to delete ClusterServiceVersion [%s] in namespace [%s], Err: %v", name, namespace, err)
	}
	logrus.Infof("Successfully deleted ClusterServiceVersion [%s] in namespace [%s]", name, namespace)
	return nil
}

// DeleteSubscription deletes Subscription resource
func DeleteSubscription(name, namespace string) error {
	logrus.Infof("Deleting Subscription [%s] in namespace [%s]", name, namespace)
	if err := opmpops.Instance().DeleteSubscription(name, namespace); err != nil {
		return fmt.Errorf("failed to delete Subscription [%s] in namespace [%s], Err: %v", name, namespace, err)
	}
	logrus.Infof("Successfully deleted Subscription [%s] in namespace [%s]", name, namespace)
	return nil
}

// DeleteOperatorGroup deletes OperatorGroup resource
func DeleteOperatorGroup(name, namespace string) error {
	logrus.Infof("Deleting OperatorGroup [%s] in namespace [%s]", name, namespace)
	if err := opmpops.Instance().DeleteOperatorGroup(name, namespace); err != nil {
		return fmt.Errorf("failed to delete OperatorGroup [%s] in namespace [%s], Err: %v", name, namespace, err)
	}
	logrus.Infof("Successfully deleted OperatorGroup [%s] in namespace [%s]", name, namespace)
	return nil
}

// DeleteCatalogSource deletes CatalogSource resource
func DeleteCatalogSource(name, namespace string) error {
	logrus.Infof("Deleting CatalogSource [%s] in namespace [%s]", name, namespace)
	if err := opmpops.Instance().DeleteCatalogSource(name, namespace); err != nil {
		return fmt.Errorf("failed to delete CatalogSource [%s] in namespace [%s], Err: %v", name, namespace, err)
	}
	logrus.Infof("Successfully deleted CatalogSource [%s] in namespace [%s]", name, namespace)
	return nil
}

// GetCurrentCSV gets currentCSV from Subscription object and returns it as a string
func GetCurrentCSV(name, namespace string) (string, error) {
	logrus.Infof("Getting currentCSV from Subscription [%s] in namespace [%s]", name, namespace)
	testSubscription, err := opmpops.Instance().GetSubscription(name, namespace)
	if err != nil {
		return "", fmt.Errorf("failed to get Subscription [%s] in namespace [%s], Err: %v", name, namespace, err)
	}
	logrus.Infof("Successfully created got Subscription [%s] in namespace [%s]", testSubscription.Name, testSubscription.Namespace)

	currentCSV := testSubscription.Status.CurrentCSV
	if len(currentCSV) == 0 {
		return "", fmt.Errorf("got empty currentCSV from Subscription [%s]", testSubscription.Name)
	}
	return currentCSV, nil
}
