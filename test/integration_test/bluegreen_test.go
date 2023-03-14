//go:build integrationtest
// +build integrationtest

package integrationtest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/test/integration_test/types"
	ci_utils "github.com/libopenstorage/operator/test/integration_test/utils"
	coreops "github.com/portworx/sched-ops/k8s/core"
	schederrors "github.com/portworx/sched-ops/k8s/errors"
	"github.com/portworx/sched-ops/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	/* crippldTestLicense is a crippled test license  (lot worse than free PX-Developer or PX-Essentials)
	   - license generated via:
	   ../bin/tools/capresponseutil -id IdentityBackOffice.bin -lifetime 157680000 -host ANY -idtype Any -server ANY -serverIdType Any /dev/stdin lic-ANY.bin << _EOF
	   INCREMENT  Nodes                portworx  1.0  07-feb-2028  3  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  Volumes              portworx  1.0  07-feb-2028  2  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  VolumeSize           portworx  1.0  07-feb-2028  1  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  HaLevel              portworx  1.0  07-feb-2028  1  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  Snapshots            portworx  1.0  07-feb-2028  1  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  EnablePlatformBare   portworx  1.0  07-feb-2028  1  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   INCREMENT  LocalVolumeAttaches  portworx  1.0  07-feb-2028  1  HOSTID=ANY VENDOR_STRING=type="Torpedo_TEST_license"  START=07-feb-2023
	   _EOF
	*/
	crippledTestLicense = `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`
	// numLicensedNodes is a number of nodes in the `crippldTestLicense`
	numLicensedNodes = 3
	// pxEnabledLabel node label enables PX install
	pxEnabledLabel = "px/enabled"
	// pxServiceLabel node label controls PX service
	pxServiceLabel = "px/service"
)

var (
	bgTestWorkerNodes []v1.Node
	bgTestSpec        = ci_utils.CreateStorageClusterTestSpecFunc(&corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "px-test-bglic"},
		Spec: corev1.StorageClusterSpec{
			DeleteStrategy: &corev1.StorageClusterDeleteStrategy{
				Type: corev1.UninstallAndWipeStorageClusterStrategyType,
			},
			// turn off sub-components, to speed up the test
			Autopilot: &corev1.AutopilotSpec{Enabled: false},
			CSI:       &corev1.CSISpec{Enabled: false},
			Stork:     &corev1.StorkSpec{Enabled: false},
			Monitoring: &corev1.MonitoringSpec{
				Prometheus: &corev1.PrometheusSpec{Enabled: false},
				Telemetry:  &corev1.TelemetrySpec{Enabled: false},
			},
		},
	})
)

var bgTestCases = []types.TestCase{
	{
		TestName:        "InstallPX",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				nodeList, err := coreops.Instance().GetNodes()
				require.NoError(t, err)

				// ensure we have more than 3 worker nodes in the cluster
				bgTestWorkerNodes = make([]v1.Node, 0, len(nodeList.Items))
				for _, node := range nodeList.Items {
					if coreops.Instance().IsNodeMaster(node) {
						continue
					}
					bgTestWorkerNodes = append(bgTestWorkerNodes, node)
				}
				require.True(t, len(bgTestWorkerNodes) > 3,
					"Need at least 4 worker nodes in the cluster  (have %d)", len(bgTestWorkerNodes))

				sort.Slice(bgTestWorkerNodes, func(i, j int) bool {
					return bgTestWorkerNodes[i].Name < bgTestWorkerNodes[j].Name
				})

				// label the nodes -- have PX start on 3 nodes
				for i, node := range bgTestWorkerNodes {
					if i < numLicensedNodes {
						// remove labels for nodes 0, 1, 2
						logrus.Infof("Will install PX on node %s", node.Name)
						err = coreops.Instance().RemoveLabelOnNode(node.Name, "px/enabled")
					} else {
						// add labels to other nodes
						logrus.Infof("Will SKIP installing PX node %s", node.Name)
						err = coreops.Instance().AddLabelOnNode(node.Name, pxEnabledLabel, "false")
					}
					require.NoError(t, err)
				}

				testSpec := tc.TestSpec(t)
				cluster, ok := testSpec.(*corev1.StorageCluster)
				require.True(t, ok)

				cluster = ci_utils.DeployAndValidateStorageCluster(cluster, ci_utils.PxSpecImages, t)
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return false },
	},
	{
		TestName:        "InstallLicense",
		TestrailCaseIDs: []string{"C54322"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				// get pods, make sure only 3 portworx PODs in the cluster
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.Equal(t, numLicensedNodes, len(pl.Items))

				logrus.Infof("Attempt license extend on Trial via node %s", pl.Items[0].Spec.NodeName)
				var stdout, stderr bytes.Buffer
				err = runInPortworxPod(&pl.Items[0],
					nil, &stdout, &stderr,
					"/bin/sh", "-c", "/opt/pwx/bin/pxctl license trial; exec /opt/pwx/bin/pxctl license extend --start")
				require.Contains(t, stdout.String(), "license extension not supported for Trial licenses")

				logrus.Infof("Installing license via node %s", pl.Items[0].Spec.NodeName)
				err = runInPortworxPod(&pl.Items[0],
					bytes.NewReader([]byte(crippledTestLicense)), &stdout, &stderr,
					"/bin/sh", "-c", "base64 -d | /opt/pwx/bin/pxctl license add /dev/stdin")
				require.Equal(t, "", stderr.String())
				require.Contains(t, stdout.String(), "Successfully updated licenses.")
				require.NoError(t, err)

				logrus.Infof("Renstalling license via node %s", pl.Items[2].Spec.NodeName)
				stdout.Reset()
				err = runInPortworxPod(&pl.Items[2],
					bytes.NewReader([]byte(crippledTestLicense)), &stdout, &stderr,
					"/bin/sh", "-c", "base64 -d | /opt/pwx/bin/pxctl license add /dev/stdin")
				require.Equal(t, "", stderr.String())
				require.Contains(t, stdout.String(), "Successfully updated licenses.")
				require.NoError(t, err)

				logrus.Infof("Checking reported license on all nodes")
				for _, p := range pl.Items {
					stdout.Reset()
					stderr.Reset()
					err = runInPortworxPod(&p, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "license", "list")
					require.Equal(t, "", stderr.String(),
						"unexpected STDERR on node %s", p.Spec.NodeName)
					require.Contains(t, stdout.String(), "PX-Enterprise Torpedo_TEST_license",
						"unexpected STDOUT on node %s", p.Spec.NodeName)
					require.NoError(t, err,
						"unexpected error on node %s", p.Spec.NodeName)
				}
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "AttemptClusterOverload",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				disabledNodes := make([]string, 0, len(bgTestWorkerNodes)/2)
				for _, node := range bgTestWorkerNodes {
					labs, err := coreops.Instance().GetLabelsOnNode(node.Name)
					require.NoError(t, err)

					if val, has := labs[pxEnabledLabel]; has && val == "false" {
						disabledNodes = append(disabledNodes, node.Name)
					}
				}
				require.NotEmpty(t, disabledNodes)

				logrus.Infof("Enabling PX on node %s", disabledNodes[0])
				err := coreops.Instance().RemoveLabelOnNode(disabledNodes[0], pxEnabledLabel)
				require.NoError(t, err)

				// verify node failed to install
				podRaw, err := task.DoRetryWithTimeout(
					taskWaitPxctlStatus(t, disabledNodes[0], "portworx", "cluster is operating at maximum capacity"),
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)
				pod, isa := podRaw.(*v1.Pod)
				require.True(t, isa)

				logrus.Infof("Stopping PX on node %s", disabledNodes[0])
				err = coreops.Instance().AddLabelOnNode(disabledNodes[0], pxServiceLabel, "stop")
				require.NoError(t, err)

				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						err = wipeNodeRunningPod(pod)
						if err != nil {
							return nil, true, err
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				logrus.Infof("Disabling PX on node %s", disabledNodes[0])
				err = coreops.Instance().AddLabelOnNode(disabledNodes[0], pxEnabledLabel, "false")
				require.NoError(t, err)
				err = coreops.Instance().RemoveLabelOnNode(disabledNodes[0], pxServiceLabel)
				require.NoError(t, err)

				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						_, err := coreops.Instance().GetPodByUID(pod.UID, pod.Namespace)
						if err == schederrors.ErrPodsNotFound {
							return nil, false, nil
						}
						return nil, true, fmt.Errorf("Waiting on POD %s to terminate", pod.Name)
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "ExtendLicenseAddNode",
		TestrailCaseIDs: []string{"C54298", "C54300", "C54307", "C54320"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				disabledNodes := make([]string, 0, len(bgTestWorkerNodes)/2)
				for _, node := range bgTestWorkerNodes {
					labs, err := coreops.Instance().GetLabelsOnNode(node.Name)
					require.NoError(t, err)

					if val, has := labs[pxEnabledLabel]; has && val == "false" {
						disabledNodes = append(disabledNodes, node.Name)
					}
				}
				require.NotEmpty(t, disabledNodes)

				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				logrus.Infof("Extending license via node %s", pl.Items[0].Spec.NodeName)
				var stdout, stderr bytes.Buffer
				err = runInPortworxPod(&pl.Items[0], nil, &stdout, &stderr,
					"/opt/pwx/bin/pxctl", "license", "extend", "--start")
				require.NoError(t, err)
				require.Contains(t, stdout.String(), "Successfully initiated license extension")

				logrus.Infof("Checking license on all nodes")
				for _, p := range pl.Items {
					stdout.Reset()
					stderr.Reset()
					err = runInPortworxPod(&p, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "status")
					assert.Empty(t, stderr.String())
					assert.Contains(t, stdout.String(), "NOTICE: License extension expires in ",
						"unexpected STDOUT @%s", p.Spec.NodeName)
					require.NoError(t, err,
						"unexpected error @%s", p.Spec.NodeName)
				}

				logrus.Infof("Enabling PX on %s", disabledNodes[0])
				err = coreops.Instance().RemoveLabelOnNode(disabledNodes[0], pxEnabledLabel)
				require.NoError(t, err)

				_, err = task.DoRetryWithTimeout(
					taskWaitPxctlStatus(t, disabledNodes[0], "portworx", "Status: PX is operational\n"),
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				pl, err = coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)
				sort.Slice(pl.Items, func(i, j int) bool {
					return pl.Items[i].Spec.NodeName < pl.Items[j].Spec.NodeName
				})

				lastPOD := pl.Items[len(pl.Items)-1]
				assert.Equal(t, disabledNodes[0], lastPOD.Spec.NodeName)

				tmpSuffix := strconv.FormatInt(time.Now().Unix(), 10)
				tmpVolName := "testVol" + tmpSuffix

				logrus.Infof("Attempt volume creation on %s", lastPOD.Spec.NodeName)
				err = runInPortworxPod(&lastPOD, nil, &stdout, &stderr,
					"/opt/pwx/bin/pxctl", "volume", "create", "--repl", "1", "--size", "3", tmpVolName)
				require.Contains(t, stdout.String(), "Volume successfully created")
				require.NoError(t, err)

				logrus.Infof("Attempt volume snapshot on %s", lastPOD.Spec.NodeName)
				err = runInPortworxPod(&lastPOD, nil, &stdout, &stderr,
					"/opt/pwx/bin/pxctl", "volume", "snapshot", "create", "--name", "snap"+tmpSuffix, tmpVolName)
				require.Contains(t, stdout.String(), "Volume snap successful")
				require.NoError(t, err)

				logrus.Infof("Cleaning up volume / snapshot on %s", lastPOD.Spec.NodeName)
				err = runInPortworxPod(&lastPOD, nil, &stdout, &stderr,
					"/bin/sh", "-c", "/opt/pwx/bin/pxctl v delete --force snap"+tmpSuffix+
						"; /opt/pwx/bin/pxctl v delete --force "+tmpVolName)
				require.Contains(t, stdout.String(), "Volume snap"+tmpSuffix+" successfully deleted")
				require.Contains(t, stdout.String(), "Volume "+tmpVolName+" successfully deleted")
				require.NoError(t, err)
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "RestartClusterWhileExtended",
		TestrailCaseIDs: []string{"C54302"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				logrus.Infof("Restarting PX on all nodes")
				for _, p := range pl.Items {
					err = coreops.Instance().AddLabelOnNode(p.Spec.NodeName, pxServiceLabel, "restart")
					require.NoError(t, err, "could not label node %s", p.Spec.NodeName)
				}

				logrus.Infof("Waiting until node-labels are cleared  (PX has restarted)")
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						cntWithLabel := 0
						for _, p := range pl.Items {
							labs, err := coreops.Instance().GetLabelsOnNode(p.Spec.NodeName)
							require.NoError(t, err, "got error retrieving labels for node %s", p.Spec.NodeName)
							if _, has := labs[pxServiceLabel]; has {
								cntWithLabel++
							}
						}
						if cntWithLabel > 0 {
							return nil, true, fmt.Errorf("waiting for %d/%d nodes to clear %s label",
								cntWithLabel, len(pl.Items), pxServiceLabel)
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				sleep4 := 30 * time.Second
				logrus.Infof("sleeping for %s before checking px-services started...", sleep4)
				time.Sleep(sleep4)

				logrus.Infof("Waiting until all PODs are ready")
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						pl, err = coreops.Instance().ListPods(map[string]string{"name": "portworx"})
						require.NoError(t, err)
						require.NotEmpty(t, pl.Items)

						cntReady, cntNotReady := 0, 0
						for _, p := range pl.Items {
							for _, st := range p.Status.ContainerStatuses {
								if st.Ready {
									cntReady++
								} else {
									cntNotReady++
								}
							}
						}
						if cntNotReady > 0 {
							return nil, true, fmt.Errorf("waiting for px-containers: %d/%d ready",
								cntReady, cntReady+cntNotReady)
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				logrus.Infof("Check status of all PX nodes")
				var stdout, stderr bytes.Buffer
				for _, p := range pl.Items {
					stdout.Reset()
					stderr.Reset()
					err = runInPortworxPod(&p, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "status")
					assert.Empty(t, stderr.String())
					assert.Contains(t, stdout.String(), "NOTICE: License extension expires in ",
						"unexpected STDOUT @%s", p.Spec.NodeName)
					require.NoError(t, err,
						"unexpected error @%s", p.Spec.NodeName)
				}
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "LicenseReinstall",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				logrus.Infof("Attempt license reinstall")
				var stdout, stderr bytes.Buffer
				err = runInPortworxPod(&pl.Items[0], bytes.NewReader([]byte(crippledTestLicense)), &stdout, &stderr,
					"/bin/sh", "-c", "base64 -d | /opt/pwx/bin/pxctl license add /dev/stdin")
				require.Equal(t, "", stderr.String())
				require.Contains(t, strings.ToLower(stdout.String()),
					"error validating license: please reduce number of nodes")
				require.Error(t, err)
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "EndExtensionWhileOverloaded",
		TestrailCaseIDs: []string{"C54315"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				logrus.Infof("End license extension while cluster over-allocated")
				var stdout, stderr bytes.Buffer
				err = runInPortworxPod(&pl.Items[0], nil, &stdout, &stderr,
					"/opt/pwx/bin/pxctl", "license", "extend", "--end")
				assert.Equal(t, "", stderr.String())
				assert.Contains(t, stdout.String(), "Successfully turned off license extension")
				require.NoError(t, err)

				logrus.Infof("Check all nodes on INVAlID license")
				for _, p := range pl.Items {
					stdout.Reset()
					stderr.Reset()
					err = runInPortworxPod(&p, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "license", "ls")
					assert.Equal(t, "", stderr.String(),
						"did no expect errors @%s", p.Spec.NodeName)
					assert.Contains(t, stdout.String(), "ERROR: too many nodes in the cluster",
						"unexpected output @%s", p.Spec.NodeName)
					require.NoError(t, err,
						"unexpected error @%s", p.Spec.NodeName)
				}
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "RestartClusterWhileOverloaded",
		TestrailCaseIDs: []string{"XXX", "XXX"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				logrus.Infof("Restarting all PX service while on INVAlID license")
				for _, p := range pl.Items {
					err = coreops.Instance().AddLabelOnNode(p.Spec.NodeName, pxServiceLabel, "restart")
					require.NoError(t, err, "could not label node %s", p.Spec.NodeName)
				}

				logrus.Infof("Waiting until labels cleared")
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						cntWithLabel := 0
						for _, p := range pl.Items {
							labs, err := coreops.Instance().GetLabelsOnNode(p.Spec.NodeName)
							require.NoError(t, err, "got error retrieving labels for node %s", p.Spec.NodeName)
							if _, has := labs[pxServiceLabel]; has {
								cntWithLabel++
							}
						}
						if cntWithLabel > 0 {
							return nil, true, fmt.Errorf("waiting for %d/%d nodes to clear %s label",
								cntWithLabel, len(pl.Items), pxServiceLabel)
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				sleep4 := 30 * time.Second
				logrus.Infof("sleeping for %s before checking px-services started...", sleep4)
				time.Sleep(sleep4)

				logrus.Infof("Waiting until PODs ready")
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						pl, err = coreops.Instance().ListPods(map[string]string{"name": "portworx"})
						require.NoError(t, err)
						require.NotEmpty(t, pl.Items)

						cntReady, cntNotReady := 0, 0
						for _, p := range pl.Items {
							for _, st := range p.Status.ContainerStatuses {
								if st.Ready {
									cntReady++
								} else {
									cntNotReady++
								}
							}
						}
						if cntNotReady > 0 {
							return nil, true, fmt.Errorf("waiting for px-containers: %d/%d ready",
								cntReady, cntReady+cntNotReady)
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return true },
	},
	{
		TestName:        "NodeDecommissionEndsOverloadedState",
		TestrailCaseIDs: []string{"C54316", "C54317"},
		TestSpec:        bgTestSpec,
		TestFunc: func(tc *types.TestCase) func(*testing.T) {
			return func(t *testing.T) {
				pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
				require.NoError(t, err)
				require.NotEmpty(t, pl.Items)

				sort.Slice(pl.Items, func(i, j int) bool {
					return pl.Items[i].Spec.NodeName < pl.Items[j].Spec.NodeName
				})
				lastNode := pl.Items[len(pl.Items)-1].Spec.NodeName

				logrus.Infof("Stopping PX on node %s", lastNode)
				err = coreops.Instance().AddLabelOnNode(lastNode, pxServiceLabel, "stop")
				require.NoError(t, err, "could not label node %s", lastNode)

				sleep4 := 30 * time.Second
				logrus.Infof("Sleeping for %s to allow portworx.service @%s to stop", sleep4, lastNode)
				time.Sleep(sleep4)

				logrus.Infof("Disabling POD on node %s", lastNode)
				err = coreops.Instance().AddLabelOnNode(lastNode, pxEnabledLabel, "false")
				require.NoError(t, err)
				err = coreops.Instance().RemoveLabelOnNode(lastNode, pxServiceLabel)
				require.NoError(t, err)

				logrus.Infof("Wiping node %s", lastNode)
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						if err = wipeNodeRunningPod(&pl.Items[len(pl.Items)-1]); err != nil {
							return nil, true, err
						}
						return nil, false, nil
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				// get NodeID for the wiped node
				var stdout, stderr bytes.Buffer
				err = runInPortworxPod(&pl.Items[0], nil, &stdout, &stderr,
					"/bin/sh", "-c", "/opt/pwx/bin/pxctl status | grep "+lastNode+" | head -1 | awk '{print $2}'")
				lastNodeID := strings.Trim(stdout.String(), "\r\n\t ")
				require.NoError(t, err)
				require.NotEmpty(t, lastNodeID)

				logrus.Infof("Decomissioning wiped PX NodeID %s via node %s", lastNodeID, pl.Items[0].Spec.NodeName)
				_, err = task.DoRetryWithTimeout(
					func() (interface{}, bool, error) {
						var stdout, stderr bytes.Buffer
						runInPortworxPod(&pl.Items[0], nil, &stdout, &stderr,
							"/opt/pwx/bin/pxctl", "cluster", "delete", lastNodeID)
						if strings.Contains(stdout.String(), " successfully deleted.") {
							logrus.Debugf("Node %s successfully decomissioned", lastNode)
							return nil, false, nil
						}
						return nil, true, fmt.Errorf("Failed node decomission: %v", err)
					},
					ci_utils.DefaultValidateDeployTimeout,
					ci_utils.DefaultValidateDeployRetryInterval,
				)
				require.NoError(t, err)

				// recheck licenses on all the nodes (except the last/wiped one)
				// note, we're recovering from `License: PX-Enterprise Torpedo_TEST_license (ERROR: too many nodes in the cluster)`
				logrus.Infof("Checking PX status on all nodes")
				for _, p := range pl.Items[:len(pl.Items)-1] {
					var stdout, stderr bytes.Buffer
					err = runInPortworxPod(&p, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "status")
					require.NoError(t, err, "unexpected error @%s", p.Spec.NodeName)
					require.Contains(t, stdout.String(), "License: PX-Enterprise Torpedo_TEST_license (expires in ",
						"unexpected content @%s", p.Spec.NodeName)
				}
			}
		},
		// ShouldSkip: func(tc *types.TestCase) bool { return false },
	},
}

func runInPortworxPod(pod *v1.Pod, in io.Reader, out, err io.Writer, command ...string) error {
	if pod == nil || len(command) <= 0 {
		return os.ErrInvalid
	}

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.Debugf("run on %s via %s: `%s`", pod.Spec.NodeName, pod.Name, strings.Join(command, " "))
	}

	return coreops.Instance().RunCommandInPodEx(&coreops.RunCommandInPodExRequest{
		command, pod.Name, "portworx", pod.Namespace, false, in, out, err,
	})
}

func wipeNodeRunningPod(pod *v1.Pod) error {
	if pod == nil {
		return os.ErrInvalid
	}
	logrus.Debugf("Wiping PX on node %s using POD %s", pod.Spec.NodeName, pod.Name)
	var stdout, stderr bytes.Buffer
	err := runInPortworxPod(pod, nil, &stdout, &stderr,
		"nsenter", "--mount=/host_proc/1/ns/mnt", "--", "/bin/sh", "-c", "pxctl sv nw --all")
	if err != nil {
		return fmt.Errorf("node-wipe failed: %s  (%s)", err,
			strings.Trim(stderr.String(), "\r\n\t "))
	} else if o := stdout.String(); !strings.Contains(o, "Wiped node successfully.") {
		logrus.
			WithField("STDOUT", o).
			WithField("STDERR", stderr.String()).
			Warnf("node-wipe unusual output detected  (see STDOUT/STDERR)")
	}
	return nil
}

func taskWaitPxctlStatus(t *testing.T, nodeName, podName, expectedOutput string) func() (interface{}, bool, error) {
	return func() (interface{}, bool, error) {
		// find POD w/ specific name, running on a given node
		pl, err := coreops.Instance().ListPods(map[string]string{"name": podName})
		require.NoError(t, err)

		var monitoredPod *v1.Pod
		for _, p := range pl.Items {
			if p.Spec.NodeName == nodeName {
				monitoredPod = &p
				break
			}
		}
		if monitoredPod == nil {
			return nil, true, fmt.Errorf("POD name=%s not yet active @%s", podName, nodeName)
		}

		// run `pxctl status` -- compare output
		var stdout, stderr bytes.Buffer
		runInPortworxPod(monitoredPod, nil, &stdout, &stderr, "/opt/pwx/bin/pxctl", "status")
		s := strings.Trim(stdout.String(), "\r\n ")
		if strings.Contains(s, expectedOutput) {
			logrus.Infof("'pxctl status' @%s got expected %q", nodeName, expectedOutput)
			return monitoredPod, false, nil
		}

		return nil, true, fmt.Errorf("'pxctl status' @%s missing %q", nodeName, expectedOutput)
	}
}

func TestBGLicenseExtension(t *testing.T) {
	for _, testCase := range bgTestCases {
		testCase.RunTest(t)
	}
}
