//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"bufio"
	"crypto/tls"
	"encoding/csv"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	test_util "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	aetosutil "github.com/libopenstorage/operator/test/integration_test/utils/aetos"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	logLevelFlag            = "log-level"
	vSphereUsernameFlag     = "portworx-vsphere-username"
	vSpherePasswordFlag     = "portworx-vsphere-password"
	dockerUsernameFlag      = "portworx-docker-username"
	dockerPasswordFlag      = "portworx-docker-password"
	isOKEFlag               = "is-oke"
	isGKEFlag               = "is-gke"
	isAKSFlag               = "is-aks"
	isEKSFlag               = "is-eks"
	isOCPFlag               = "is-ocp"
	pxKVDBSpecFlag          = "portworx-kvdb-spec"
	pxDeviceSpecsFlag       = "portworx-device-specs"
	pxUpgradeHopsURLsFlag   = "px-upgrade-hops-url-list"
	operatorUpgradeHopsFlag = "operator-upgrade-hops-image-list"
	pxOperatorTagFlag       = "operator-image-tag"
	cloudProviderFlag       = "cloud-provider"
	pxEnvVarsFlag           = "portworx-env-vars"
	pxCustomAnnotationsFlag = "portworx-custom-annotations"
	pxImageOverrideFlag     = "portworx-image-override"
	pxSpecGenURLFlag        = "portworx-spec-gen-url"
	pxNamespaceFlag         = "px-namespace"
	enableDashBoardFlag     = "enable-dash"
	userFlag                = "user"
	testTypeFlag            = "test-type"
	testDescriptionFlag     = "test-desc"
	testTagsFlag            = "test-tags"
	testSetIDFlag           = "testset-id"
	testBranchFlag          = "branch"
	testProductFlag         = "product"
)

var dash *aetosutil.Dashboard

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		logrus.Errorf("Setup failed with error: %v", err)
		os.Exit(1)
	}
	logrus.Infof("Setup completed successfully, starting tests...")
	dash.TestSetBegin(dash.TestSet)
	exitCode := m.Run()
	types.TestReporterInstance().PrintTestResult()
	dash.TestSetEnd()
	os.Exit(exitCode)
}

func setup() error {
	// Parse flags
	var pxUpgradeHopsURLs string
	var operatorUpgradeHopsImages string
	var logLevel string
	var err error
	var enableDash bool
	var user, testBranch, testProduct, testType, testDescription, testTags string
	var testsetID int

	flag.StringVar(&ci_utils.PxDockerUsername,
		dockerUsernameFlag,
		"",
		"Portworx Docker username used for pull")
	flag.StringVar(&ci_utils.PxDockerPassword,
		dockerPasswordFlag,
		"",
		"Portworx Docker password used for pull")
	flag.StringVar(&ci_utils.PxVsphereUsername,
		vSphereUsernameFlag,
		"",
		"Encoded base64 Portworx vSphere username")
	flag.StringVar(&ci_utils.PxVspherePassword,
		vSpherePasswordFlag,
		"",
		"Encoded base64 Portworx vSphere password")
	flag.StringVar(&ci_utils.PxSpecGenURL,
		pxSpecGenURLFlag,
		"",
		"Portworx Spec Generator URL, defines what Portworx version will be deployed")
	flag.StringVar(&ci_utils.PxImageOverride,
		pxImageOverrideFlag,
		"",
		"Portworx Image override, defines what Portworx version will be deployed")
	flag.StringVar(&pxUpgradeHopsURLs,
		pxUpgradeHopsURLsFlag,
		"",
		"List of Portworx Spec Generator URLs separated by commas used for upgrade hops")
	flag.StringVar(&ci_utils.PxOperatorTag,
		pxOperatorTagFlag,
		"",
		"Operator tag that is needed for deploying PX Operator via Openshift MarketPlace")
	flag.StringVar(&operatorUpgradeHopsImages,
		operatorUpgradeHopsFlag,
		"",
		"List of Portworx Operator images separated by commas used for operator upgrade hops")
	flag.StringVar(&ci_utils.CloudProvider,
		cloudProviderFlag,
		"",
		"Type of cloud provider")
	flag.StringVar(&ci_utils.PxEnvVars,
		pxEnvVarsFlag,
		"",
		"List of comma separated environment variables that will be added to StorageCluster spec")
	flag.StringVar(&ci_utils.PxCustomAnnotations,
		pxCustomAnnotationsFlag,
		"",
		"List of comma separated custom annotations that will be added to StorageCluster spec")
	flag.StringVar(&ci_utils.PxDeviceSpecs,
		pxDeviceSpecsFlag,
		"",
		"List of `;` separated PX device specs")
	flag.StringVar(&ci_utils.PxKvdbSpec,
		pxKVDBSpecFlag,
		"",
		"PX KVDB device spec")
	flag.BoolVar(&ci_utils.IsOcp,
		isOCPFlag,
		false,
		"Is this OpenShift")
	flag.BoolVar(&ci_utils.IsEks,
		isEKSFlag,
		false,
		"Is this EKS")
	flag.BoolVar(&ci_utils.IsAks,
		isAKSFlag,
		false,
		"Is this AKS")
	flag.BoolVar(&ci_utils.IsGke,
		isGKEFlag,
		false,
		"Is this GKE")
	flag.BoolVar(&ci_utils.IsOke,
		isOKEFlag,
		false,
		"Is this OKE")
	flag.StringVar(&logLevel,
		logLevelFlag,
		"",
		"Log level")
	flag.StringVar(&ci_utils.PxNamespace,
		pxNamespaceFlag,
		"kube-system",
		"Namespace where the operator will be deployed")
	flag.BoolVar(&enableDash,
		enableDashBoardFlag,
		true,
		"To enable/disable aetos dashboard reporting")
	flag.StringVar(&user,
		userFlag,
		"nouser",
		"user name running the tests")
	flag.StringVar(&testDescription,
		testDescriptionFlag,
		"Operator Integration Workflows",
		"test suite description")
	flag.StringVar(&testType,
		testTypeFlag,
		"integration",
		"test types like system-test,functional,integration")
	flag.StringVar(&testTags,
		testTagsFlag,
		"",
		"tags running the tests. Eg: key1:val1,key2:val2")
	flag.IntVar(&testsetID,
		testSetIDFlag,
		0,
		"testset id to post the results")
	flag.StringVar(&testBranch,
		testBranchFlag,
		"master",
		"branch of the product")
	flag.StringVar(&testProduct,
		testProductFlag,
		"PxOperator",
		"Portworx product under test")
	flag.Parse()

	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	// Setup Aetos
	err = initAetosDashboard(enableDash, user, testDescription, testType, testTags, testBranch, testProduct, testsetID)
	if err != nil {
		return err
	}

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

	pxOperatorDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx-operator",
			Namespace: ci_utils.PxNamespace,
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

func initAetosDashboard(enableDash bool, user string, testDescription string, testType string,
	testTags string, testBranch string, testProduct string, testsetID int) error {
	logrus.Infof("Initializing Aetos Dashboard!")
	dash = aetosutil.Get()
	if enableDash && !isDashboardReachable() {
		enableDash = false
		logrus.Infof("Aetos Dashboard is not reachable. Disabling dashboard reporting.")
	}

	dash.IsEnabled = enableDash
	testSet := aetosutil.TestSet{
		User:        user,
		Product:     testProduct,
		Description: testDescription,
		Branch:      testBranch,
		TestType:    testType,
		Tags:        make(map[string]string),
		Status:      aetosutil.NOTSTARTED,
	}
	if testTags != "" {
		tags, err := splitCsv(testTags)
		if err != nil {
			logrus.Fatalf("failed to parse tags: %v. err: %v", testTags, err)
		} else {
			for _, tag := range tags {
				var key, value string
				if !strings.Contains(tag, ":") {
					logrus.Infof("Invalid tag %s. Please provide tag in key:value format skipping provided tag", tag)
				} else {
					key = strings.SplitN(tag, ":", 2)[0]
					value = strings.SplitN(tag, ":", 2)[1]
					testSet.Tags[key] = value
				}
			}
		}
	}

	/*
		Get TestSetID based on below precedence
		1. Check if user has passed in command line and use
		2. Check if user has set it has an env variable and use
		3. Check if build.properties available with TestSetID
		4. If not present create a new one
	*/
	val, ok := os.LookupEnv("DASH_UID")
	if testsetID != 0 {
		dash.TestSetID = testsetID
	} else if ok && (val != "" && val != "0") {
		_, err := strconv.Atoi(val)
		logrus.Infof(fmt.Sprintf("Using TestSetID: %s set as enviornment variable", val))
		if err != nil {
			logrus.Warnf("Failed to convert environment testset id  %v to int, err: %v", val, err)
		}
	} else {
		fileName := "/build.properties"
		readFile, err := os.Open(fileName)
		if err == nil {
			fileScanner := bufio.NewScanner(readFile)
			fileScanner.Split(bufio.ScanLines)
			for fileScanner.Scan() {
				line := fileScanner.Text()
				if strings.Contains(line, "DASH_UID") {
					testsetToUse := strings.Split(line, "=")[1]
					logrus.Infof("Using TestSetID: %s found in build.properties", testsetToUse)
					dash.TestSetID, err = strconv.Atoi(testsetToUse)
					if err != nil {
						logrus.Errorf("Error in getting DASH_UID variable, %v", err)
					}
					break
				}
			}
		}
	}
	if testsetID != 0 {
		dash.TestSetID = testsetID
		err := os.Setenv("DASH_UID", fmt.Sprint(testsetID))
		if err != nil {
			logrus.Errorf("Error in setting DASH_UID as env variable, %v", err)
			return err
		}
	}

	dash.TestSet = &testSet
	return nil
}

func isDashboardReachable() bool {
	timeout := 15 * time.Second
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	aboutURL := strings.Replace(aetosutil.DashBoardBaseURL, "dashboard", "datamodel/about", -1)
	logrus.Infof("Checking URL: %s", aboutURL)
	response, err := client.Get(aboutURL)

	if err != nil {
		logrus.Warn(err.Error())
		return false
	}
	if response.StatusCode == 200 {
		return true
	}
	return false
}

func splitCsv(in string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(in))
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil || len(records) < 1 {
		return []string{}, err
	} else if len(records) > 1 {
		return []string{}, fmt.Errorf("multiline CSV not supported")
	}
	return records[0], err
}
