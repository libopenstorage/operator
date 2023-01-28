package utils

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	op_v1 "github.com/operator-framework/api/pkg/operators/v1"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/portworx/sched-ops/k8s/core"
	opmpops "github.com/portworx/sched-ops/k8s/operatormarketplace"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	core_v1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultOperatorRegistryImageName = "docker.io/portworx/px-operator-registry"
	defaultCatalogSourceName         = "test-catalog"
	defaultCatalogSourceNamespace    = "openshift-marketplace"
	defaultOperatorGroupName         = "test-operator-group"
	defaultSubscriptionName          = "test-portworx-certified"

	defaultPxVsphereSecretName = "px-vsphere-secret"

	getInstallPlanListTimeout       = 10 * time.Minute
	getInstallPlanListRetryInterval = 10 * time.Second

	getPxVsphereSecretTimeout       = 10 * time.Minute
	getPxVsphereSecretRetryInterval = 5 * time.Second
)

// DeployAndValidatePxOperatorViaMarketplace deploys and validates PX Operator via Openshift Marketplace
func DeployAndValidatePxOperatorViaMarketplace(operatorVersion string) error {
	// Parsing PX Operator tag version
	opRegistryTag := parsePxOperatorImageTagForMarketplace(operatorVersion)

	// Deploy CatalogSource
	opRegistryImage := fmt.Sprintf("%s:%s", defaultOperatorRegistryImageName, opRegistryTag)
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
	testSubscription, err := DeploySubscription(defaultSubscriptionName, PxNamespace, opRegistryTag, testCatalogSource)
	if err != nil {
		return err
	}

	// Approve InstallPlan
	if err := ApproveInstallPlan(PxNamespace, opRegistryTag, testSubscription); err != nil {
		return err
	}

	// Validate PX Operator deployment and version
	if err := ValidatePxOperatorDeploymentAndVersion(opRegistryTag, PxNamespace); err != nil {
		return err
	}

	logrus.Infof("Successfully deployed PX Operator in namespace [%s]", PxNamespace)
	return nil
}

// UpdateAndValidatePxOperatorViaMarketplace updates and validates PX Operator and its resources via Openshift Marketplace
func UpdateAndValidatePxOperatorViaMarketplace(operatorVersion string) error {
	// Parsing PX Operator tag version
	opRegistryTag := parsePxOperatorImageTagForMarketplace(operatorVersion)

	// Edit CatalogSource with the new PX Operator version
	updateParamFunc := func(testCatalogSource *v1alpha1.CatalogSource) *v1alpha1.CatalogSource {
		testCatalogSource.Spec.Image = fmt.Sprintf("%s:%s", defaultOperatorRegistryImageName, opRegistryTag)
		return testCatalogSource
	}

	testCatalogSource := &v1alpha1.CatalogSource{}
	testCatalogSource.Name = defaultCatalogSourceName
	testCatalogSource.Namespace = defaultCatalogSourceNamespace
	if _, err := UpdateCatalogSource(testCatalogSource, updateParamFunc); err != nil {
		return err
	}

	// Get subscription
	testSubscription, err := opmpops.Instance().GetSubscription(defaultSubscriptionName, PxNamespace)
	if err != nil {
		return err
	}

	// Wait for new InstallPlan with that new version in clusterServiceVersionNames and approve it
	if err := ApproveInstallPlan(PxNamespace, opRegistryTag, testSubscription); err != nil {
		return err
	}

	// Validate PX Operator deployment and version
	// NOTE: In Marketplace PX Operator deployment, we will use opRegistryTag to compare as we do not actually see the -dev tag
	// and we get image tag from the OPERATOR_CONDITION_NAME value in the portworx-operator deployment, which is same as registry tag
	if err := ValidatePxOperatorDeploymentAndVersion(opRegistryTag, PxNamespace); err != nil {
		return err
	}

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
func DeployOperatorGroup(name, namespace string) (*op_v1.OperatorGroup, error) {
	logrus.Infof("Create test OperatorGroup [%s] in namespace [%s]", name, namespace)
	testOperatorGroupTemplate := &op_v1.OperatorGroup{
		Spec: op_v1.OperatorGroupSpec{
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
		installPlanList, err := opmpops.Instance().ListInstallPlans(namespace)
		if err != nil {
			return nil, true, fmt.Errorf("failed to get list of InstallPlans in namespace [%s], Err: %v", namespace, err)
		}

		if installPlanList != nil && len(installPlanList.Items) > 0 {
			logrus.Infof("Successfully got list of InstallPlans in namespace [%s]", namespace)
			for _, installPlan := range installPlanList.Items {
				if subscription.Name == installPlan.OwnerReferences[0].Name {
					for _, clusterServiceVersions := range installPlan.Spec.ClusterServiceVersionNames {
						if clusterServiceVersions == fmt.Sprintf("portworx-operator.v%s", opTag) {
							logrus.Infof("Found the correct InstallPlan [%s] in namespace [%s]", installPlan.Name, installPlan.Namespace)
							logrus.Infof("Update Approved value to true for InstallPlan [%s]", installPlan.Name)
							installPlan.Spec.Approved = true
							_, err = opmpops.Instance().UpdateInstallPlan(&installPlan)
							if err != nil {
								return nil, true, fmt.Errorf("failed to update InstallPlan [%s], Err: %v", installPlan.Name, err)
							}
							logrus.Infof("Successfully Approved InstallPlan [%s]", installPlan.Name)
							return nil, false, nil
						}
					}
				}
			}
			return nil, true, fmt.Errorf("failed to find the correct InstallPlan with OwnerReference for Subscription [%s] and/or PX Operator version [%s]", subscription.Name, opTag)
		}
		return nil, true, fmt.Errorf("failed to find any InstallPlans in namespace [%s]", namespace)
	}

	_, err := task.DoRetryWithTimeout(t, getInstallPlanListTimeout, getInstallPlanListRetryInterval)
	if err != nil {
		return err
	}
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

// UpdateCatalogSource updates CatalogSource resource
func UpdateCatalogSource(catalogSource *v1alpha1.CatalogSource, f func(*v1alpha1.CatalogSource) *v1alpha1.CatalogSource) (*v1alpha1.CatalogSource, error) {
	logrus.Infof("Updating CatalogSource [%s] in namespace [%s]", catalogSource.Name, catalogSource.Namespace)
	// Get CatalogSource
	liveCatalogSource, err := opmpops.Instance().GetCatalogSource(catalogSource.Name, catalogSource.Namespace)
	if err != nil {
		return nil, err
	}

	newCatalogSource := f(liveCatalogSource)

	latestCatalogSource, err := opmpops.Instance().UpdateCatalogSource(newCatalogSource)
	if err != nil {
		return nil, fmt.Errorf("failed to update CatalogSource [%s] in namespace [%s], Err: %v", catalogSource.Name, catalogSource.Namespace, err)
	}
	logrus.Infof("Successfully updated CatalogSource [%s] in namespace [%s]", latestCatalogSource.Name, latestCatalogSource.Namespace)
	return latestCatalogSource, nil
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

func parsePxOperatorImageTagForMarketplace(currentOperatorImageTag string) string {
	// Registry tag doesn't use -dev and -dev is not used in the image tags when deployed via Marketplace, need to remove -dev from tag
	newOperatorImageTag := currentOperatorImageTag
	if strings.Contains(currentOperatorImageTag, "-dev") {
		newOperatorImageTag = strings.Split(currentOperatorImageTag, "-dev")[0]
		logrus.Infof("Got Dev PX Operator tag [%s], converting it to [%s], as Openshift Marketplace doesn't use this format for image tags", currentOperatorImageTag, newOperatorImageTag)
	}
	return newOperatorImageTag
}

// CreatePxVsphereSecret creates secret for PX to authenticate with vSphere
func CreatePxVsphereSecret(namespace, pxVsphereUsername, pxVspherePassword string) error {
	logrus.Infof("Creating secret [%s]", defaultPxVsphereSecretName)
	name, _ := base64.StdEncoding.DecodeString(pxVsphereUsername)
	password, _ := base64.StdEncoding.DecodeString(pxVspherePassword)

	secret := core_v1.Secret{
		ObjectMeta: meta_v1.ObjectMeta{
			Name:      defaultPxVsphereSecretName,
			Namespace: namespace,
		},
		Type: "Opaque",
		Data: map[string][]byte{
			"VSPHERE_USER":     name,
			"VSPHERE_PASSWORD": password,
		},
	}

	t := func() (interface{}, bool, error) {
		if _, err := core.Instance().CreateSecret(&secret); err != nil {
			if k8s_errors.IsAlreadyExists(err) {
				logrus.Warnf("Secret [%s] already exists, reusing it.", secret.Name)
				return nil, false, nil
			}
			return nil, true, fmt.Errorf("failed to create secret [%s], Err: %v", secret.Name, err)
		}
		return nil, false, nil
	}

	if _, err := task.DoRetryWithTimeout(t, getPxVsphereSecretTimeout, getPxVsphereSecretRetryInterval); err != nil {
		return err
	}

	return nil
}
