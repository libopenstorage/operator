//go:build integrationtest
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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	flag.StringVar(&ci_utils.PxVsphereUsername,
		"portworx-vsphere-username",
		"",
		"Encoded base64 Portworx vSphere username")
	flag.StringVar(&ci_utils.PxVspherePassword,
		"portworx-vsphere-password",
		"",
		"Encoded base64 Portworx vSphere password")
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
	flag.StringVar(&ci_utils.PxOperatorTag,
		"operator-image-tag",
		"",
		"Operator tag that is needed for deploying PX Operator via Openshift MarketPlace")
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
	flag.StringVar(&ci_utils.PxCustomAnnotations,
		"portworx-custom-annotations",
		"",
		"List of comma separated custom annotations that will be added to StorageCluster spec")
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
	flag.BoolVar(&ci_utils.IsAks,
		"is-aks",
		false,
		"Is this AKS")
	flag.BoolVar(&ci_utils.IsGke,
		"is-gke",
		false,
		"Is this GKE")
	flag.BoolVar(&ci_utils.IsOke,
		"is-oke",
		false,
		"Is this OKE")
	flag.StringVar(&logLevel,
		"log-level",
		"",
		"Log level")
	flag.Parse()

	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	ci_utils.K8sVersion, err = test_util.GetK8SVersion()
	if err != nil {
		return err
	}

	ci_utils.PxSpecImages, err = test_util.GetImagesFromVersionURL(ci_utils.PxSpecGenURL, ci_utils.K8sVersion)
	if err != nil {
		return err
	}

	if pxUpgradeHopsURLs != "" {
		ci_utils.PxUpgradeHopsURLList = strings.Split(pxUpgradeHopsURLs, ",")
	}

	if operatorUpgradeHopsImages != "" {
		ci_utils.OperatorUpgradeHopsImageList = strings.Split(operatorUpgradeHopsImages, ",")
	}

	// TODO: Right now we do not pass any namespaces parameters
	// and we currently do not support initial operator deployment to be outside of kube-system
	// will add this functionality in PTX-17953
	pxOperatorDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-operator",
			Namespace: "kube-system",
		},
	}

	ci_utils.PxOperatorVersion, err = ci_utils.GetPXOperatorVersion(pxOperatorDep)
	if err != nil {
		logrus.Warnf("failed to discover installed [%s] in [%s] namespace and get its version, Err: %v", pxOperatorDep.Name, pxOperatorDep.Namespace, err)
		if len(ci_utils.PxOperatorTag) != 0 && ci_utils.IsOcp {
			logrus.Infof("PX Operator tag was passed in --operator-image-tag [%s], this tag will be used to deploy PX Operator via Openshift Marketplace", ci_utils.PxOperatorTag)
		} else {
			return fmt.Errorf("operator [%s] is not deployed in [%s] namespace, cannot proceed", pxOperatorDep.Name, pxOperatorDep.Namespace)
		}
	}

	return nil
}
