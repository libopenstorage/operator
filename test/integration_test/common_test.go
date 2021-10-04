// +build integrationtest

package integrationtest

import (
	"flag"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"

	"github.com/portworx/sched-ops/k8s/operator"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var (
	pxDockerUsername string
	pxDockerPassword string

	pxSpecGenURL string

	pxUpgradeHopsURLList string

	logLevel string
)

const (
	// specDir is a directory with all the specs
	specDir = "./operator-test"

	// pxNamespace is a default namespace for StorageCluster
	pxNamespace = "kube-system"

	// defaultValidateDeployTimeout is a default timeout for deployment validation
	defaultValidateDeployTimeout = 900 * time.Second
	// defaultValidateDeployRetryInterval is a default retry interval for deployment validation
	defaultValidateDeployRetryInterval = 30 * time.Second
	// defaultValidateUpgradeTimeout is a default timeout for upgrade validation
	defaultValidateUpgradeTimeout = 1400 * time.Second
	// defaultValidateUpgradeRetryInterval is a default retry interval for upgrade validation
	defaultValidateUpgradeRetryInterval = 60 * time.Second
	// defaultValidateUninstallTimeout is a default timeout for uninstall validation
	defaultValidateUninstallTimeout = 900 * time.Second
	// defaultValidateUninstallRetryInterval is a default retry interval for uninstall validation
	defaultValidateUninstallRetryInterval = 30 * time.Second
)

// node* is to be used in the Node section of the StorageCluster spec. node0 will select the
// alphabetically 1st PX node, node1 will select the 2nd, and so on
const (
	nodeReplacePrefix = "replaceWithNodeNumber"

	node0 = nodeReplacePrefix + "0"
	node1 = nodeReplacePrefix + "1"
	node2 = nodeReplacePrefix + "2"
)

// TestrailCase describes one test case on TestRail, which will
// instantiate the given StorageCluster spec, check that it
// started/failed to start correctly, and then remove it.
type TestrailCase struct {
	CaseIDs                 []string
	Spec                    corev1.StorageClusterSpec
	ShouldStartSuccessfully bool
}

func (trc *TestrailCase) PopulateStorageCluster(cluster *corev1.StorageCluster) error {
	cluster.Name = makeDNS1123Compatible(strings.Join(trc.CaseIDs, "-"))
	cluster.Spec.CloudStorage = trc.Spec.CloudStorage
	cluster.Spec.Kvdb = trc.Spec.Kvdb

	names, err := testutil.GetExpectedPxNodeNameList(cluster)
	if err != nil {
		return err
	}
	// Sort for consistent order between multiple tests
	sort.Strings(names)

	// For each node, if the selector looks like "replaceWithNodeNumberN", replace it with
	// the name of the Nth eligible Portworx node
	for i := range trc.Spec.Nodes {
		if !strings.HasPrefix(trc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix) {
			continue
		}

		num := strings.TrimPrefix(trc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix)
		parsedNum, err := strconv.Atoi(num)
		if err != nil {
			return err
		}

		if parsedNum >= len(names) {
			return fmt.Errorf("requested node index %d is larger than eligible worker node count %d", parsedNum, len(names))
		}

		trc.Spec.Nodes[i].Selector.NodeName = names[parsedNum]
	}

	cluster.Spec.Nodes = trc.Spec.Nodes

	return nil
}

func TestMain(m *testing.M) {
	flag.StringVar(&pxDockerUsername,
		"portworx-docker-username",
		"",
		"Portworx Docker username used for pull")
	flag.StringVar(&pxDockerPassword,
		"portworx-docker-password",
		"",
		"Portworx Docker password used for pull")
	flag.StringVar(&pxSpecGenURL,
		"portworx-spec-gen-url",
		"",
		"Portworx Spec Generator URL, defines what Portworx version will be deployed")
	flag.StringVar(&pxUpgradeHopsURLList,
		"upgrade-hops-url-list",
		"",
		"List of Portworx Spec Generator URLs separated by commas used for upgrade hops")
	flag.StringVar(&logLevel,
		"log-level",
		"",
		"Log level")
	flag.Parse()
	if err := setup(); err != nil {
		logrus.Errorf("Setup failed with error: %v", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func setup() error {
	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	return nil
}

// Here we make StorageCluster object and add all the common basic parameters that all StorageCluster should have
func constructStorageCluster(specGenURL string, imageListMap map[string]string) (*corev1.StorageCluster, error) {
	cluster := &corev1.StorageCluster{}

	// Set Portworx Image
	cluster.Spec.Image = imageListMap["version"]

	// Set Namespace
	cluster.Namespace = pxNamespace

	// Populate default Env Vars
	if err := populateDefaultEnvVars(cluster, specGenURL); err != nil {
		return nil, err
	}

	return cluster, nil
}

func createStorageClusterFromSpec(filename string) (*corev1.StorageCluster, error) {
	filepath := path.Join(specDir, filename)
	scheme := runtime.NewScheme()
	cluster := &corev1.StorageCluster{}
	if err := k8sutil.ParseObjectFromFile(filepath, scheme, cluster); err != nil {
		return nil, err
	}
	return createStorageCluster(cluster)
}

func createStorageCluster(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	return operator.Instance().CreateStorageCluster(cluster)
}

func updateStorageCluster(cluster *corev1.StorageCluster, specGenURL string, imageListMap map[string]string) (*corev1.StorageCluster, error) {
	// Get StorageCluster
	cluster, err := operator.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil {
		return nil, err
	}

	// Set Portworx Image
	cluster.Spec.Image = imageListMap["version"]

	// Populate default Env Vars
	if err = populateDefaultEnvVars(cluster, specGenURL); err != nil {
		return nil, err
	}

	return operator.Instance().UpdateStorageCluster(cluster)
}

func populateDefaultEnvVars(cluster *corev1.StorageCluster, specGenURL string) error {
	// Set release manifest URL and Docker credentials in case of edge-install.portworx.com
	if strings.Contains(specGenURL, "edge") {
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(specGenURL)
		if err != nil {
			return err
		}

		envVarList := []v1.EnvVar{}

		// Add release manifest URL to Env Vars
		envVarList = append(envVarList, []v1.EnvVar{{Name: testutil.PxReleaseManifestURLEnvVarName, Value: releaseManifestURL}}...)

		// Add Portworx Docker credentials to Env Vars
		if pxDockerUsername != "" && pxDockerPassword != "" {
			envVarList = append(envVarList, []v1.EnvVar{{Name: testutil.PxRegistryUserEnvVarName, Value: pxDockerUsername}, {Name: testutil.PxRegistryPasswordEnvVarName, Value: pxDockerPassword}}...)
		}

		cluster = addEnvVarToStorageCluster(envVarList, cluster)
	}

	return nil
}

func addEnvVarToStorageCluster(envVarList []v1.EnvVar, cluster *corev1.StorageCluster) *corev1.StorageCluster {
	cluster.Spec.CommonConfig.Env = append(cluster.Spec.CommonConfig.Env, envVarList...)
	return cluster
}

func validateStorageClusterComponents(cluster *corev1.StorageCluster) error {
	// TODO: Validate expected components are deployed and running
	// TODO: Validate the components are running with expected configuration
	return nil
}

// makeDNS1123Compatible will make the given string a valid DNS1123 name, which is the same
// validation that Kubernetes uses for its object names.
// Borrowed from
// https://gitlab.com/gitlab-org/gitlab-runner/-/blob/0e2ae0001684f681ff901baa85e0d63ec7838568/executors/kubernetes/util.go#L268
func makeDNS1123Compatible(name string) string {
	const (
		DNS1123NameMaximumLength         = 63
		DNS1123NotAllowedCharacters      = "[^-a-z0-9]"
		DNS1123NotAllowedStartCharacters = "^[^a-z0-9]+"
	)

	name = strings.ToLower(name)

	nameNotAllowedChars := regexp.MustCompile(DNS1123NotAllowedCharacters)
	name = nameNotAllowedChars.ReplaceAllString(name, "")

	nameNotAllowedStartChars := regexp.MustCompile(DNS1123NotAllowedStartCharacters)
	name = nameNotAllowedStartChars.ReplaceAllString(name, "")

	if len(name) > DNS1123NameMaximumLength {
		name = name[0:DNS1123NameMaximumLength]
	}

	return name
}
