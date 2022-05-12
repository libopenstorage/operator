// +build integrationtest

package integrationtest

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	test_util "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		logrus.Errorf("Setup failed with error: %v", err)
		os.Exit(1)
	}
	exitCode := m.Run()
	types.TestReporterInstance().PrintTestResult()
	os.Exit(exitCode)
}

func setup() error {
	// Parse flags
	var pxUpgradeHopsURLs string
	var operatorUpgradeHopsImages string
	var logLevel string
	var err error
	flag.StringVar(&ci_utils.PxDockerUsername,
		"portworx-docker-username",
		"",
		"Portworx Docker username used for pull")
	flag.StringVar(&ci_utils.PxDockerPassword,
		"portworx-docker-password",
		"",
		"Portworx Docker password used for pull")
	flag.StringVar(&ci_utils.PxSpecGenURL,
		"portworx-spec-gen-url",
		"",
		"Portworx Spec Generator URL, defines what Portworx version will be deployed")
	flag.StringVar(&ci_utils.PxImageOverride,
		"portworx-image-override",
		"",
		"Portworx Image override, defines what Portworx version will be deployed")
	flag.StringVar(&pxUpgradeHopsURLs,
		"px-upgrade-hops-url-list",
		"",
		"List of Portworx Spec Generator URLs separated by commas used for upgrade hops")
	flag.StringVar(&operatorUpgradeHopsImages,
		"operator-upgrade-hops-image-list",
		"",
		"List of Portworx Operator images separated by commas used for operator upgrade hops")
	flag.StringVar(&ci_utils.CloudProvider,
		"cloud-provider",
		"",
		"Type of cloud provider")
	flag.StringVar(&ci_utils.PxEnvVars,
		"portworx-env-vars",
		"",
		"List of comma separated environment variables that will be added to StorageCluster spec")
	flag.StringVar(&ci_utils.PxDeviceSpecs,
		"portworx-device-specs",
		"",
		"List of `;` separated PX device specs")
	flag.StringVar(&ci_utils.PxKvdbSpec,
		"portworx-kvdb-spec",
		"",
		"PX KVDB device spec")
	flag.BoolVar(&ci_utils.IsOcp,
		"is-ocp",
		false,
		"Is this OpenShift")
	flag.BoolVar(&ci_utils.IsEks,
		"is-eks",
		false,
		"Is this EKS")
	flag.StringVar(&logLevel,
		"log-level",
		"",
		"Log level")
	flag.Parse()

	ci_utils.K8sVersion, err = test_util.GetK8SVersion()
	if err != nil {
		return err
	}

	ci_utils.PxSpecImages, err = test_util.GetImagesFromVersionURL(ci_utils.PxSpecGenURL, ci_utils.K8sVersion)
	if err != nil {
		return err
	}

	if len(pxUpgradeHopsURLs) != 0 {
		ci_utils.PxUpgradeHopsURLList = strings.Split(pxUpgradeHopsURLs, ",")
	}

	if len(operatorUpgradeHopsImages) != 0 {
		ci_utils.OperatorUpgradeHopsImageList = strings.Split(operatorUpgradeHopsImages, ",")
	}

	ci_utils.PxOperatorVersion, err = ci_utils.GetPXOperatorVersion()
	if err != nil {
		return fmt.Errorf("failed to discover installed portworx operator version: %v", err)
	}

	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	return nil
}
