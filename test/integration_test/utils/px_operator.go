package utils

import (
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"

	appops "github.com/portworx/sched-ops/k8s/apps"
)

const (
	nextReleaseTag = "1.9.0-dev"
)

var (
	// PxOperatorVer1_7 portworx-operator 1.7 minimum version
	PxOperatorVer1_7, _ = version.NewVersion("1.7-")
	// PxOperatorVer1_8 portworx-operator 1.8 minimum version
	PxOperatorVer1_8, _ = version.NewVersion("1.8-")
)

// TODO: Install portworx-operator in test automation

// GetPXOperatorVersion returns the portworx operator version found
func GetPXOperatorVersion() (*version.Version, error) {
	pxImageTag, err := getPXOperatorImageTag()
	if err != nil {
		return nil, err
	}

	// We may run the automation on operator installed using private images,
	// so assume we are testing the latest operator version if failed to parse the tag
	opVersion, err := version.NewVersion(pxImageTag)
	if err != nil {
		logrus.WithError(err).Warnf("Failed to parse portworx-operator tag to version, assuming next release tag")
		opVersion, _ = version.NewVersion(nextReleaseTag)
	}

	logrus.Infof("Testing portworx-operator version: %s", opVersion.String())
	return opVersion, nil
}

func getPXOperatorImageTag() (string, error) {
	deployment, err := appops.Instance().GetDeployment(PortworxOperatorDeploymentName, PxNamespace)
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

	return strings.Split(image, ":")[1], nil
}
