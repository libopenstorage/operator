package utils

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	appops "github.com/portworx/sched-ops/k8s/apps"
	"github.com/portworx/sched-ops/task"
)

const (
	pxOperatorMasterVersion  = "9.9.9.9"
	pxOperatorDeploymentName = "portworx-operator"
)

var (
	// PxOperatorVer1_7 portworx-operator 1.7 minimum version
	PxOperatorVer1_7, _ = version.NewVersion("1.7-")
	// PxOperatorVer1_8 portworx-operator 1.8 minimum version
	PxOperatorVer1_8, _ = version.NewVersion("1.8-")
	// PxOperatorVer1_8_1 portworx-operator 1.8.1 minimum version
	PxOperatorVer1_8_1, _ = version.NewVersion("1.8.1-")
)

// TODO: Install portworx-operator in test automation

// GetPXOperatorVersion returns the portworx operator version found
func GetPXOperatorVersion() (*version.Version, error) {
	imageTag, err := getPXOperatorImageTag()
	if err != nil {
		return nil, err
	}
	// tag is not a valid version, e.g. commit sha in PR automation builds "1a6a788" can be parsed to "1.0.0-a6a788"
	if !strings.Contains(imageTag, ".") {
		logrus.Errorf("Operator tag %s is not a valid version tag, assuming its latest and setting it to %s", imageTag, pxOperatorMasterVersion)
		imageTag = pxOperatorMasterVersion
	}
	// We may run the automation on operator installed using private images,
	// so assume we are testing the latest operator version if failed to parse the tag
	opVersion, err := version.NewVersion(imageTag)
	if err != nil {
		logrus.WithError(err).Warnf("Failed to parse portworx-operator tag to version, assuming its latest and setting it to %s", pxOperatorMasterVersion)
		opVersion, _ = version.NewVersion(pxOperatorMasterVersion)
	}

	logrus.Infof("Testing portworx-operator version [%s]", opVersion.String())
	return opVersion, nil
}

func getPXOperatorImageTag() (string, error) {
	deployment, err := appops.Instance().GetDeployment(PortworxOperatorDeploymentName, PxNamespace)
	if err != nil {
		return "", err
	}

	var tag string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == PortworxOperatorContainerName {
			if strings.Contains(container.Image, "registry.connect.redhat.com") { // PX Operator deployed via Openshift Marketplace will have "registry.connect.redhat.com" as part of image
				for _, env := range container.Env {
					if env.Name == "OPERATOR_CONDITION_NAME" {
						logrus.Infof("Looks like portworx-operator was installed via Openshift Marketplace, image [%s]", container.Image)
						tag = strings.Split(env.Value, ".v")[1]
						return tag, nil
					}
				}
			} else {
				logrus.Infof("Get portworx-operator image installed [%s]", container.Image)
				tag = strings.Split(container.Image, ":")[1]
				return tag, nil
			}
		}
	}

	return "", fmt.Errorf("failed to find PX Operator tag")
}

// GetPxOperatorImage return PX Operator image
func GetPxOperatorImage() (string, error) {
	deployment, err := GetPxOperatorDeployment()
	if err != nil {
		return "", err
	}

	var image string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == PortworxOperatorContainerName {
			image = container.Image
			logrus.Infof("Get portworx-operator image installed: %s", image)
			break
		}
	}

	return image, nil
}

// GetPxOperatorDeployment return PX Operator deployment
func GetPxOperatorDeployment() (*appsv1.Deployment, error) {
	return appops.Instance().GetDeployment(PortworxOperatorDeploymentName, PxNamespace)
}

// UpdateAndValidatePxOperator update and validate PX Operator deployment
func UpdateAndValidatePxOperator(pxOperator *appsv1.Deployment, f func(*appsv1.Deployment) *appsv1.Deployment, t *testing.T) *appsv1.Deployment {
	livePxOperator, err := appops.Instance().GetDeployment(pxOperator.Name, pxOperator.Namespace)
	require.NoError(t, err)

	newPxOperator := f(livePxOperator)

	latestLivePxOperator, err := UpdatePxOperator(newPxOperator)
	require.NoError(t, err)

	err = appops.Instance().ValidateDeployment(latestLivePxOperator, DefaultValidateUpdateTimeout, DefaultValidateUpdateRetryInterval)
	require.NoError(t, err)

	return latestLivePxOperator
}

// UpdatePxOperator update PX Operator deploymnent
func UpdatePxOperator(pxOperator *appsv1.Deployment) (*appsv1.Deployment, error) {
	return appops.Instance().UpdateDeployment(pxOperator)
}

// ValidatePxOperator validatea PX Operator deployment is created
func ValidatePxOperator(namespace string) (*appsv1.Deployment, error) {
	pxOperatorDeployment := &appsv1.Deployment{}
	pxOperatorDeployment.Name = pxOperatorDeploymentName
	pxOperatorDeployment.Namespace = namespace
	if err := appops.Instance().ValidateDeployment(pxOperatorDeployment, DefaultValidateDeployTimeout, DefaultValidateDeployRetryInterval); err != nil {
		return nil, err
	}

	logrus.Infof("Successfuly validated PX Operator deployment")
	return pxOperatorDeployment, nil
}

// ValidatePxOperatorDeploymentAndVersion validates PX Operator deployment is created and compares PX Operator version to the expected
func ValidatePxOperatorDeploymentAndVersion(expectedOpVersion, namespace string) error {
	pxOperatorDeployment := &appsv1.Deployment{}
	pxOperatorDeployment.Name = pxOperatorDeploymentName
	pxOperatorDeployment.Namespace = namespace

	t := func() (interface{}, bool, error) {
		if err := appops.Instance().ValidateDeployment(pxOperatorDeployment, DefaultValidateDeployTimeout, DefaultValidateDeployRetryInterval); err != nil {
			return nil, true, err
		}

		// Get PX Operator image tag
		opVersion, err := getPXOperatorImageTag()
		if err != nil {
			return nil, true, err
		}

		if opVersion != expectedOpVersion {
			return nil, true, fmt.Errorf("failed to validate PX Operator version, Expected version: %s, actual version: %s", expectedOpVersion, opVersion)
		}

		logrus.Infof("Successfuly validated PX Operator deployment and version [%s]", opVersion)
		return nil, false, nil
	}

	_, err := task.DoRetryWithTimeout(t, getInstallPlanListTimeout, getInstallPlanListRetryInterval)
	if err != nil {
		return err
	}
	return nil
}

// ValidatePxOperatorDeleted validate PX Operator deployment is deleted
func ValidatePxOperatorDeleted(namespace string) (*appsv1.Deployment, error) {
	pxOperatorDeployment := &appsv1.Deployment{}
	pxOperatorDeployment.Name = pxOperatorDeploymentName
	pxOperatorDeployment.Namespace = namespace
	if err := appops.Instance().ValidateTerminatedDeployment(pxOperatorDeployment, DefaultValidateDeployTimeout, DefaultValidateDeployRetryInterval); err != nil {
		return nil, err
	}
	return pxOperatorDeployment, nil
}
