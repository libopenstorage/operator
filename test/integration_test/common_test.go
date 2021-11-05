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

	"github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/libopenstorage/operator/drivers/storage/portworx"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s/operator"
)

var (
	pxDockerUsername string
	pxDockerPassword string

	pxSpecGenURL    string
	pxImageOverride string
	pxSpecImages    map[string]string

	pxUpgradeHopsURLList []string
)

const (
	// specDir is a directory with all the specs
	specDir = "./operator-test"

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
	flag.StringVar(&pxImageOverride,
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

	pxSpecImages, err = testutil.GetImagesFromVersionURL(pxSpecGenURL)
	if err != nil {
		return err
	}

	pxUpgradeHopsURLList = strings.Split(pxUpgradeHopsURLs, ",")

	// Set log level
	logrusLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logrus.SetLevel(logrusLevel)
	logrus.SetOutput(os.Stdout)

	return nil
}

func (tc *TestCase) PopulateStorageCluster(cluster *corev1.StorageCluster) error {
	cluster.Name = makeDNS1123Compatible(strings.Join(tc.TestrailCaseIDs, "-"))
	cluster.Spec.CloudStorage = tc.Spec.CloudStorage
	cluster.Spec.Kvdb = tc.Spec.Kvdb

	names, err := testutil.GetExpectedPxNodeNameList(cluster)
	if err != nil {
		return err
	}
	// Sort for consistent order between multiple tests
	sort.Strings(names)

	// For each node, if the selector looks like "replaceWithNodeNumberN", replace it with
	// the name of the Nth eligible Portworx node
	for i := range tc.Spec.Nodes {
		if !strings.HasPrefix(tc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix) {
			continue
		}

		num := strings.TrimPrefix(tc.Spec.Nodes[i].Selector.NodeName, nodeReplacePrefix)
		parsedNum, err := strconv.Atoi(num)
		if err != nil {
			return err
		}

		if parsedNum >= len(names) {
			return fmt.Errorf("requested node index %d is larger than eligible worker node count %d", parsedNum, len(names))
		}

		tc.Spec.Nodes[i].Selector.NodeName = names[parsedNum]
	}

	cluster.Spec.Nodes = tc.Spec.Nodes

	return nil
}

// Here we make StorageCluster object and add all the common basic parameters that all StorageCluster should have
func constructStorageCluster(cluster *corev1.StorageCluster, specGenURL string, specImages map[string]string) error {
	// Set Portworx Image
	cluster.Spec.Image = specImages["version"]

	// Set Namespace
	cluster.Namespace = PxNamespace

	// Populate default Env Vars
	if err := populateDefaultEnvVars(cluster, specGenURL); err != nil {
		return err
	}

	return nil
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
	logrus.Infof("Create StorageCluster %s in %s", cluster.Name, cluster.Namespace)
	portworx.SetPortworxDefaults(cluster)
	return operator.Instance().CreateStorageCluster(cluster)
}

func updateStorageCluster(cluster *corev1.StorageCluster, specGenURL string, imageListMap map[string]string) (*corev1.StorageCluster, error) {
	logrus.Infof("Update StorageCluster %s in %s", cluster.Name, cluster.Namespace)
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
	var envVarList []v1.EnvVar

	// Set release manifest URL and Docker credentials in case of edge-install.portworx.com
	if strings.Contains(specGenURL, "edge") {
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(specGenURL)
		if err != nil {
			return err
		}

		// Add release manifest URL to Env Vars
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxReleaseManifestURLEnvVarName, Value: releaseManifestURL})
	}

	// Add Portworx image properties, if specified
	if pxDockerUsername != "" && pxDockerPassword != "" {
		envVarList = append(envVarList,
			[]v1.EnvVar{
				{Name: testutil.PxRegistryUserEnvVarName, Value: pxDockerUsername},
				{Name: testutil.PxRegistryPasswordEnvVarName, Value: pxDockerPassword},
			}...)
	}
	if pxImageOverride != "" {
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxImageEnvVarName, Value: pxImageOverride})
	}

	addEnvVarToStorageCluster(envVarList, cluster)

	return nil
}

// addEnvVarToStorageCluster will overwrite existing or add new env variables
func addEnvVarToStorageCluster(envVarList []v1.EnvVar, cluster *corev1.StorageCluster) {
	envMap := make(map[string]v1.EnvVar)
	var newEnvVarList []v1.EnvVar
	for _, env := range cluster.Spec.Env {
		envMap[env.Name] = env
	}
	for _, env := range envVarList {
		envMap[env.Name] = env
	}
	for _, env := range envMap {
		newEnvVarList = append(newEnvVarList, env)
	}
	cluster.Spec.Env = newEnvVarList
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

// getPxVersionFromSpecGenURL gets the px version to install or upgrade,
// e.g. return version 2.9 for https://edge-install.portworx.com/2.9
func getPxVersionFromSpecGenURL(url string) *version.Version {
	splitURL := strings.Split(url, "/")
	v, _ := version.NewVersion(splitURL[len(splitURL)-1])
	return v
}
