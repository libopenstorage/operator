// +build integrationtest

package integrationtest

import (
	"flag"
	"fmt"
	"os"
	"path"
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

	"github.com/portworx/torpedo/drivers/node"
	_ "github.com/portworx/torpedo/drivers/node/ssh"
	"github.com/portworx/torpedo/drivers/scheduler"
	_ "github.com/portworx/torpedo/drivers/scheduler/k8s"
	"github.com/portworx/torpedo/drivers/volume"
	_ "github.com/portworx/torpedo/drivers/volume/aws"
	_ "github.com/portworx/torpedo/drivers/volume/azure"
	_ "github.com/portworx/torpedo/drivers/volume/gce"
	_ "github.com/portworx/torpedo/drivers/volume/generic_csi"
	_ "github.com/portworx/torpedo/drivers/volume/portworx"
	. "github.com/portworx/torpedo/tests"
)

var (
	pxDockerUsername string
	pxDockerPassword string

	pxSpecGenURL string

	pxUpgradeHopsURLList string

	logLevel string
)

const (
	// nodeDriverNmae is a name for node driver
	nodeDriverName = "ssh"

	// volumeDriverName is a name for storage driver
	volumeDriverName = "pxd"

	// schedulerDriverNam is a name of the schedule driver
	schedulerDriverName = "k8s"

	// specDir is a directory with all the specs
	specDir = "./specs"

	appsDir = "./apps"

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

var schedulerDriver scheduler.Driver
var nodeDriver node.Driver
var volumeDriver volume.Driver

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

	if schedulerDriver, err = scheduler.Get(schedulerDriverName); err != nil {
		return fmt.Errorf("Error getting scheduler driver %v: %v", schedulerDriverName, err)
	}

	err = schedulerDriver.RescanSpecs(appsDir, volumeDriverName)
	if err != nil {
		return fmt.Errorf("Unable to parse app spec dir: %v", err)
	}

	if nodeDriver, err = node.Get(nodeDriverName); err != nil {
		return fmt.Errorf("Error getting node driver %v: %v", nodeDriverName, err)
	}

	if err = nodeDriver.Init(node.InitOptions{
		SpecDir: appsDir,
	}); err != nil {
		return fmt.Errorf("Error initializing node driver %v: %v", nodeDriverName, err)
	}

	if volumeDriver, err = volume.Get(volumeDriverName); err != nil {
		return fmt.Errorf("Error getting volume driver %v: %v", volumeDriverName, err)
	}

	if err = schedulerDriver.Init(scheduler.InitOptions{SpecDir: "specs",
		NodeDriverName: nodeDriverName,
		VolDriverName:  volumeDriverName,
		//SecretConfigMapName: authTokenConfigMap,
		//CustomAppConfig:     customAppConfig,
	}); err != nil {
		return fmt.Errorf("Error initializing scheduler driver %v: %v", schedulerDriverName, err)
	}

	return nil
}

func setupApp() ([]*scheduler.Context, error) {
	fmt.Printf("KOKADBG: setupApp(): START\n")
	var contexts []*scheduler.Context
	scaleFactor := 1

	for i := 0; i < scaleFactor; i++ {
		contexts = make([]*scheduler.Context, 0)
		contexts = append(contexts, ScheduleApplications(fmt.Sprintf("setupteardown-%d", i))...)
	}

	ValidateApplications(contexts)
	fmt.Printf("KOKADBG: setupApp(): END\n")
	return contexts, nil
}

func teardownApp(contexts []*scheduler.Context) error {
	fmt.Printf("KOKADBG: teardownApp(): START\n")
	opts := make(map[string]bool)
	opts[scheduler.OptionsWaitForResourceLeakCleanup] = true

	for _, ctx := range contexts {
		TearDownContext(ctx, opts)
	}

	fmt.Printf("KOKADBG: teardownApp(): END\n")
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
