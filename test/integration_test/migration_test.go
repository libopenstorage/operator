//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	operatorops "github.com/portworx/sched-ops/k8s/operator"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiscore "k8s.io/kubernetes/pkg/apis/core"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
)

const (
	operatorRestartTimeout       = 5 * time.Minute
	operatorRestartRetryInterval = 5 * time.Second
	clusterCreationTimeout       = 5 * time.Minute
	clusterCreationRetryInterval = 5 * time.Second
	labelKeyPXEnabled            = "px/enabled"
)

var migrationTestCases = []types.TestCase{
	{
		TestName: "SimpleMigration",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/simple-daemonset.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: BasicMigration,
	},
	{
		TestName: "SimpleMigrationWithoutNodeAffinity",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/simple-daemonset.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: MigrationWithoutNodeAffinity,
	},
	{
		TestName: "AllComponentsEnabled",
		TestSpec: func(t *testing.T) interface{} {
			objects, err := ci_utils.ParseSpecs("migration/daemonset-with-all-components.yaml")
			require.NoError(t, err)
			return objects
		},
		TestFunc: MigrationWithAllComponents,
		AppSpecs: func(t *testing.T) []runtime.Object {
			appSpecs, err := ci_utils.ParseApplicationSpecs("nginx")
			require.NoError(t, err)
			return appSpecs
		},
	},
}

func TestDaemonSetMigration(t *testing.T) {
	for _, tc := range migrationTestCases {
		tc.ShouldSkip = shouldSkipMigrationTests
		tc.RunTest(t)
	}
}

func BasicMigration(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		objects, ok := testSpec.([]runtime.Object)
		require.True(t, ok)

		pxDaemonSet, objects := extractPortworxDaemonSetFromObjects(objects)
		require.NotNil(t, pxDaemonSet)

		nodeNameWithLabel := ci_utils.AddLabelToRandomNode(t, labelKeyPXEnabled, ci_utils.LabelValueFalse)

		testMigration(t, pxDaemonSet, objects, tc.AppSpecs(t))

		ci_utils.RemoveLabelFromNode(t, nodeNameWithLabel, labelKeyPXEnabled)
	}
}

func MigrationWithoutNodeAffinity(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		testSpec := tc.TestSpec(t)
		objects, ok := testSpec.([]runtime.Object)
		require.True(t, ok)

		pxDaemonSet, objects := extractPortworxDaemonSetFromObjects(objects)
		require.NotNil(t, pxDaemonSet)

		// Remove affinity from portworx daemonset
		pxDaemonSet.Spec.Template.Spec.Affinity = nil

		testMigration(t, pxDaemonSet, objects, tc.AppSpecs(t))
	}
}

func MigrationWithAllComponents(tc *types.TestCase) func(*testing.T) {
	return func(t *testing.T) {
		objects, err := ci_utils.ParseSpecs("migration/prometheus-operator.yaml")
		require.NoError(t, err)

		logrus.Infof("Installing prometheus operator")
		err = ci_utils.CreateObjects(objects)
		require.NoError(t, err)

		logrus.Infof("Waiting for prometheus operator to be ready")
		dep := &appsv1.Deployment{}
		dep.Name = "prometheus-operator"
		dep.Namespace = apiscore.NamespaceSystem
		err = appops.Instance().ValidateDeployment(dep, ci_utils.DefaultValidateDeployTimeout, 5*time.Second)
		require.NoError(t, err)

		alertManagerSecret, err := ci_utils.ParseSpecs("migration/alertmanager-secret.yaml")
		require.NoError(t, err)

		logrus.Infof("Creating alert manager secret")
		err = ci_utils.CreateObjects(alertManagerSecret)
		require.NoError(t, err)

		MigrationWithoutNodeAffinity(tc)(t)

		logrus.Infof("Deleting alert manager secret")
		err = ci_utils.DeleteObjects(alertManagerSecret)
		require.NoError(t, err)

		logrus.Infof("Validate old prometheus operator is removed as well")
		err = ci_utils.ValidateObjectsAreTerminated(objects, true)
		require.NoError(t, err)
	}
}

func testMigration(t *testing.T, ds *appsv1.DaemonSet, objects, appSpecs []runtime.Object) {
	err := ci_utils.UpdatePortworxDaemonSetImages(ds)
	require.NoError(t, err)

	err = updateComponentDeploymentImages(objects)
	require.NoError(t, err)

	logrus.Infof("Creating portworx objects")
	err = ci_utils.CreateObjects(objects)
	require.NoError(t, err)

	logrus.Infof("Creating portworx daemonset")
	_, err = appops.Instance().CreateDaemonSet(ds, metav1.CreateOptions{})
	require.NoError(t, err)

	logrus.Infof("Waiting for portworx daemonset to be ready")
	err = appops.Instance().ValidateDaemonSet(ds.Name, ds.Namespace, ci_utils.DefaultValidateDeployTimeout)
	require.NoError(t, err)

	if len(appSpecs) != 0 {
		logrus.Infof("Create application workloads")
		err = ci_utils.CreateObjects(appSpecs)
		require.NoError(t, err)

		logrus.Infof("Validate application workloads")
		err = ci_utils.ValidateApplicationWorkload(appSpecs,
			ci_utils.DefaultValidateApplicationTimeout, ci_utils.DefaultValidateApplicationRetryInterval)
		require.NoError(t, err)
	}

	// It's possible portworx daemonset becomes ready but node initialization is not finished yet,
	// so give it 10 more seconds given it happens rarely
	time.Sleep(10 * time.Second)

	logrus.Infof("Restarting portworx operator to trigger migration")
	restartPortworxOperator(t)

	logrus.Infof("Validate equivalient StorageCluster has been created")
	cluster, err := validateStorageClusterIsCreatedForDaemonSet(ds)
	require.NoError(t, err)

	logrus.Infof("Approving migration for StorageCluster %s", cluster.Name)
	cluster, err = approveMigration(cluster)
	require.NoError(t, err)

	logrus.Infof("Validate original spec is deleted")
	err = appops.Instance().ValidateDaemonSetIsTerminated(ds.Name, ds.Namespace, ci_utils.DefaultValidateUpgradeTimeout)
	require.NoError(t, err)

	logrus.Infof("Validate StorageCluster %s", cluster.Name)
	err = testutil.ValidateStorageCluster(ci_utils.PxSpecImages, cluster,
		ci_utils.DefaultValidateDeployTimeout, ci_utils.DefaultValidateDeployRetryInterval, true)
	require.NoError(t, err)

	if len(appSpecs) != 0 {
		logrus.Infof("Validate application workloads after migration")
		err = ci_utils.ValidateApplicationWorkload(appSpecs,
			ci_utils.DefaultValidateApplicationTimeout, ci_utils.DefaultValidateApplicationRetryInterval)
		require.NoError(t, err)
		logrus.Infof("Delete application workloads")
		err = ci_utils.DeleteObjects(appSpecs)
		require.NoError(t, err)
		logrus.Infof("Validate application workloads are deleted")
		err = ci_utils.ValidateObjectsAreTerminated(appSpecs, false)
		require.NoError(t, err)
	}

	ci_utils.UninstallAndValidateStorageCluster(cluster, t)

	logrus.Infof("Validate migration labels have been removed from nodes")
	nodeList, err := coreops.Instance().GetNodes()
	for _, node := range nodeList.Items {
		_, exists := node.Labels[constants.LabelPortworxDaemonsetMigration]
		require.False(t, exists, "%s label should not have been present on node %s",
			constants.LabelPortworxDaemonsetMigration, node.Name)
	}

	logrus.Infof("Validate old portworx objects are removed")
	err = ci_utils.ValidateObjectsAreTerminated(objects, true)
	require.NoError(t, err)

	logrus.Infof("Remove remaining portworx objects")
	err = ci_utils.DeleteObjects(objects)
	require.NoError(t, err)
	err = ci_utils.ValidateObjectsAreTerminated(objects, false)
	require.NoError(t, err)
}

func approveMigration(cluster *corev1.StorageCluster) (*corev1.StorageCluster, error) {
	liveCluster, err := operatorops.Instance().GetStorageCluster(cluster.Name, cluster.Namespace)
	if err != nil {
		return nil, err
	}
	liveCluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	return operatorops.Instance().UpdateStorageCluster(liveCluster)
}

func validateStorageClusterIsCreatedForDaemonSet(ds *appsv1.DaemonSet) (*corev1.StorageCluster, error) {
	clusterName := getClusterNameFromDaemonSet(ds)
	t := func() (interface{}, bool, error) {
		cluster, err := operatorops.Instance().GetStorageCluster(clusterName, ds.Namespace)
		if err != nil {
			return nil, true, err
		}

		value, ok := cluster.Annotations[constants.AnnotationMigrationApproved]
		if !ok {
			return nil, true, fmt.Errorf("%s annotation is not present on StorageCluster", constants.AnnotationMigrationApproved)
		}
		approved, err := strconv.ParseBool(value)
		if err != nil {
			return nil, true, fmt.Errorf("invalid value %s assigned to annotation %s. %v",
				value, constants.AnnotationMigrationApproved, err)
		} else if approved {
			return nil, true, fmt.Errorf("migration should not be auto-approved")
		}

		return cluster, false, nil
	}
	stc, err := task.DoRetryWithTimeout(t, clusterCreationTimeout, clusterCreationRetryInterval)
	if err != nil {
		return nil, err
	}
	cluster := stc.(*corev1.StorageCluster)

	if err := validateStorageClusterFromDaemonSet(cluster, ds); err != nil {
		return nil, err
	}
	return cluster, nil
}

func getClusterNameFromDaemonSet(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "portworx" {
			for i, arg := range c.Args {
				if arg == "-c" {
					return c.Args[i+1]
				}
			}
		}
	}
	return ""
}

func extractPortworxDaemonSetFromObjects(objects []runtime.Object) (*appsv1.DaemonSet, []runtime.Object) {
	var pxDaemonSet *appsv1.DaemonSet
	remainingObjects := []runtime.Object{}
	for _, obj := range objects {
		if daemonSet, ok := obj.(*appsv1.DaemonSet); ok && daemonSet.Name == "portworx" {
			pxDaemonSet = daemonSet.DeepCopy()
		} else {
			remainingObjects = append(remainingObjects, obj.DeepCopyObject())
		}
	}
	return pxDaemonSet, remainingObjects
}

func updateComponentDeploymentImages(objects []runtime.Object) error {
	k8sVersion, err := k8sutil.GetVersion()
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if dep, ok := obj.(*appsv1.Deployment); ok {
			if dep.Name == "stork-scheduler" || dep.Name == "portworx-pvc-controller" {
				image := strings.Split(dep.Spec.Template.Spec.Containers[0].Image, ":")[0]
				dep.Spec.Template.Spec.Containers[0].Image = image + ":v" + k8sVersion.String()
			} else if dep.Name == "stork" || dep.Name == "autopilot" {
				dep.Spec.Template.Spec.Containers[0].Image = ci_utils.PxSpecImages[dep.Name]
			} else if dep.Name == "px-csi-ext" {
				for i := 0; i < len(dep.Spec.Template.Spec.Containers); i++ {
					c := &dep.Spec.Template.Spec.Containers[i]
					if c.Name == "csi-external-provisioner" {
						c.Image = ci_utils.PxSpecImages["csiProvisioner"]
						if util.GetImageMajorVersion(c.Image) >= 2 {
							c.Args = []string{
								"--v=3",
								"--csi-address=$(ADDRESS)",
								"--leader-election=true",
								"--default-fstype=ext4",
								"--extra-create-metadata=true",
							}
						}
					} else if c.Name == "csi-snapshotter" {
						c.Image = ci_utils.PxSpecImages["csiSnapshotter"]
					} else if c.Name == "csi-resizer" {
						c.Image = ci_utils.PxSpecImages["csiResizer"]
					} else if c.Name == "csi-snapshot-controller" {
						c.Image = ci_utils.PxSpecImages["csiSnapshotController"]
					} else if c.Name == "csi-attacher" {
						c.Image = ci_utils.PxSpecImages["csiAttacher"]
					}
				}
			} else if dep.Name == "px-metrics-collector" {
				for i := 0; i < len(dep.Spec.Template.Spec.Containers); i++ {
					c := &dep.Spec.Template.Spec.Containers[i]
					if c.Name == "collector" {
						c.Image = ci_utils.PxSpecImages["metricsCollector"]
					} else if c.Name == "envoy" {
						c.Image = ci_utils.PxSpecImages["metricsCollectorProxy"]
					}
				}
			}
		}
	}
	return nil
}

func restartPortworxOperator(t *testing.T) {
	dep, err := appops.Instance().GetDeployment(ci_utils.PortworxOperatorDeploymentName, apiscore.NamespaceSystem)
	require.NoError(t, err)

	pods, err := appops.Instance().GetDeploymentPods(dep)
	require.NoError(t, err)

	err = coreops.Instance().DeletePods(pods, false)
	require.NoError(t, err)

	err = appops.Instance().ValidateDeployment(dep, operatorRestartTimeout, operatorRestartRetryInterval)
	require.NoError(t, err)
}

func shouldSkipMigrationTests(tc *types.TestCase) bool {
	k8sVersion, _ := k8sutil.GetVersion()
	return k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_22)
}

func validateStorageClusterFromDaemonSet(
	cluster *corev1.StorageCluster,
	ds *appsv1.DaemonSet,
) error {
	for _, c := range ds.Spec.Template.Spec.Containers {
		var cerr error
		switch c.Name {
		case "portworx":
			cerr = validateStorageClusterFromPortworxContainer(cluster, &c)
		case "csi-node-driver-registrar":
			if !cluster.Spec.CSI.Enabled {
				cerr = fmt.Errorf("csi should have been enabled")
			}
		case "telemetry":
			telemetryEnabled := cluster.Spec.Monitoring != nil &&
				cluster.Spec.Monitoring.Telemetry != nil &&
				cluster.Spec.Monitoring.Telemetry.Enabled
			ccmGoSupported := pxutil.GetPortworxVersion(cluster).GreaterThanOrEqual(pxutil.MinimumPxVersionCCMGO)
			if ccmGoSupported && telemetryEnabled {
				cerr = fmt.Errorf("telemetry should have been disabled")
			} else if !ccmGoSupported && !telemetryEnabled {
				cerr = fmt.Errorf("telemetry should have been enabled")
			}
		}
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

func validateStorageClusterFromPortworxContainer(
	cluster *corev1.StorageCluster,
	container *v1.Container,
) error {
	var startPort uint32
	rtOpts := map[string]string{}
	for i := 0; i < len(container.Args); i++ {
		arg := container.Args[i]
		if arg == "-r" {
			port, err := strconv.Atoi(container.Args[i+1])
			if err != nil {
				return err
			}
			startPort = uint32(port)
		} else if arg == "-rt_opts" {
			opts := strings.Split(container.Args[i+1], ",")
			for _, opt := range opts {
				s := strings.Split(opt, "=")
				rtOpts[s[0]] = s[1]
			}
			i++
		}
	}

	if startPort != 0 {
		if cluster.Spec.StartPort == nil {
			return fmt.Errorf("start port do not match: expected: %d, actual: nil", startPort)
		} else if *(cluster.Spec.StartPort) != startPort {
			return fmt.Errorf("start port do not match: expected: %d, actual: %d", startPort, *cluster.Spec.StartPort)
		}
	}

	if len(rtOpts) == 0 {
		rtOpts = nil
	}
	if !reflect.DeepEqual(rtOpts, cluster.Spec.RuntimeOpts) {
		return fmt.Errorf("runtime options do not match: expected: %+v, actual: %+v", rtOpts, cluster.Spec.RuntimeOpts)
	}

	envMap := map[string]*v1.EnvVar{}
	for _, env := range cluster.Spec.Env {
		envMap[env.Name] = env.DeepCopy()
	}
	for _, dsEnv := range container.Env {
		if dsEnv.Name == "PX_TEMPLATE_VERSION" ||
			dsEnv.Name == "PORTWORX_CSIVERSION" ||
			dsEnv.Name == "CSI_ENDPOINT" ||
			dsEnv.Name == "NODE_NAME" ||
			dsEnv.Name == "PX_NAMESPACE" {
			continue
		}
		if env, present := envMap[dsEnv.Name]; !present {
			return fmt.Errorf("expected env: %s to be present in StorageCluster", dsEnv.Name)
		} else if !reflect.DeepEqual(dsEnv, *env) {
			return fmt.Errorf("environment variable do not match: expected: %+v, actual: %+v", dsEnv, env)
		}
	}

	return nil
}
