// +build integrationtest

package integrationtest

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	test_util "github.com/libopenstorage/operator/pkg/util/test"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		logrus.Errorf("Setup failed with error: %v", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func setup() error {
	// Parse flags
	var pxUpgradeHopsURLs string
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
		"upgrade-hops-url-list",
		"",
		"List of Portworx Spec Generator URLs separated by commas used for upgrade hops")
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

	ci_utils.PxUpgradeHopsURLList = strings.Split(pxUpgradeHopsURLs, ",")

	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	return nil
}
