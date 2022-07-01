package dryrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	goversion "github.com/hashicorp/go-version"
	ocp_configv1 "github.com/openshift/api/config/v1"
	"github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/apis"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/migration"
	"github.com/libopenstorage/operator/pkg/mock"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	inttestutil "github.com/libopenstorage/operator/test/integration_test/utils"
)

const (
	defaultOutputFolder      = "portworxSpecs"
	defaultFileAllComponents = "AllSpecs.yaml"
)

var (
	skipComponents = map[string]bool{
		// CRD component registration is stuck at CRD validation as fake client does not set status of CRD.
		// Potential fixes:
		//   1. remove the validation -- need more tests to see if it's safe to remove it.
		//   2. create a thread to monitor the CRD and update status for dry run.
		component.PortworxCRDComponentName: true,
		// Disruption budget component needs to talk to portworx SDK,
		// it also needs px to be deployed before it can get correct disruption budget, however
		// dry run is before px being deployed.
		component.DisruptionBudgetComponentName: true,
	}

	skipObjectKinds = map[string]bool{
		"Namespace":          true,
		"ClusterRole":        true,
		"ClusterRoleBinding": true,
		"Role":               true,
		"RoleBinding":        true,
		"ServiceAccount":     true,
	}
)

// DryRun is a struct for portworx installation dry run
type DryRun struct {
	mockController    *storagecluster.Controller
	realControllerMgr manager.Manager
	mockControllerMgr *mock.MockManager
	// Point to real k8s cluster.
	realClient   client.Client
	mockClient   client.Client
	mockDriver   storage.Driver
	cluster      *corev1.StorageCluster
	outputFolder string
}

// Init performs required initialization
func (d *DryRun) Init(kubeconfig, outputFolder, storageClusterFile string) error {
	logrus.Debugf("command line args, kubeconfig: %s, outputFolder: %s, storageClusterFile: %s", kubeconfig, outputFolder, storageClusterFile)
	var err error

	k8sVersion := "v1.22.0"
	d.realClient, err = d.getRealK8sClient(kubeconfig)
	if err != nil {
		logrus.WithError(err).Warningf("Could not connect to k8s cluster, will run in standalone mode.")
	} else {
		ver, err := testutil.GetK8SVersion()
		if err != nil {
			logrus.WithError(err).Error("failed to get k8s version")
		} else {
			k8sVersion = ver
			logrus.Infof("k8s version is %s", k8sVersion)
			minVer, err := goversion.NewVersion("1.16")
			if err != nil {
				return fmt.Errorf("error parsing version '1.16': %v", err)
			}

			curVer, err := goversion.NewVersion(k8sVersion)
			if err != nil {
				return fmt.Errorf("error parsing version %s: %v", k8sVersion, err)
			}

			if !curVer.GreaterThanOrEqual(minVer) {
				return fmt.Errorf("unsupported k8s version %v, please upgrade k8s to %s+ before migration", k8sVersion, minVer)
			}
		}
	}

	versionClient := fakek8sclient.NewSimpleClientset()
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: k8sVersion,
	}
	coreops.SetInstance(coreops.New(versionClient))

	mockClientSet := fakeextclient.NewSimpleClientset()
	apiextensions.SetInstance(apiextensions.New(mockClientSet))

	d.mockDriver, err = storage.Get(pxutil.DriverName)
	if err != nil {
		logrus.WithError(err).Fatalf("failed to get mock driver")
	}

	d.mockClient = testutil.FakeK8sClient()
	if err = d.mockDriver.Init(d.mockClient, runtime.NewScheme(), record.NewFakeRecorder(100)); err != nil {
		logrus.WithError(err).Fatalf("failed to initialize mock driver")
	}

	mockCtrl := gomock.NewController(nil)
	mockRecorder := record.NewFakeRecorder(100)
	d.mockControllerMgr = mock.NewMockManager(mockCtrl)
	mockCache := mock.NewMockCache(mockCtrl)
	mockCache.EXPECT().
		IndexField(gomock.Any(), gomock.Any(), "nodeName", gomock.Any()).
		Return(nil).
		AnyTimes()
	d.mockControllerMgr.EXPECT().GetClient().Return(d.mockClient).AnyTimes()
	d.mockControllerMgr.EXPECT().GetScheme().Return(scheme.Scheme).AnyTimes()
	d.mockControllerMgr.EXPECT().GetEventRecorderFor(gomock.Any()).Return(mockRecorder).AnyTimes()
	d.mockControllerMgr.EXPECT().GetConfig().Return(&rest.Config{
		Host:    "127.0.0.1",
		APIPath: "fake",
	}).AnyTimes()
	d.mockControllerMgr.EXPECT().SetFields(gomock.Any()).Return(nil).AnyTimes()
	d.mockControllerMgr.EXPECT().GetCache().Return(mockCache).AnyTimes()
	d.mockControllerMgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	d.mockControllerMgr.EXPECT().GetLogger().Return(log.Log.WithName("test")).AnyTimes()

	d.mockController = &storagecluster.Controller{Driver: d.mockDriver}
	d.mockController.SetEventRecorder(mockRecorder)
	err = d.mockController.Init(d.mockControllerMgr)
	if err != nil {
		logrus.WithError(err).Fatal("failed to initialize controller")
	}
	d.mockController.SetKubernetesClient(d.mockClient)

	if outputFolder != "" {
		d.outputFolder = outputFolder
	}

	d.cluster, err = d.getStorageCluster(storageClusterFile)
	if err != nil {
		logrus.WithError(err).Fatal("failed to get storage cluster.")
	}
	// Mock migration approved.
	d.cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	d.cluster.Annotations[constants.AnnotationPauseComponentMigration] = "false"
	d.cluster.Status.Phase = constants.PhaseMigrationInProgress
	// Disable storage due to the driver will talk to portworx SDK to get storage node information.
	// However dryrun is before installation so there won't be useful information.
	// It will also disable PDB.
	// We will generate portworx pod spec by calling the API directly.
	d.cluster.Annotations[constants.AnnotationDisableStorage] = "true"
	// Make a fake cluster ID as metrics collector waits for the UID before deploying.
	d.cluster.Status.ClusterUID = uuid.New().String()

	return nil
}

// Execute dry run for portworx installation
func (d *DryRun) Execute() error {
	err := d.mockClient.Create(context.TODO(), d.cluster)
	if err != nil {
		return err
	}

	if err = d.simulateK8sNode(); err != nil {
		return err
	}

	if err = d.installAllComponents(); err != nil {
		return err
	}

	objs, err := d.getAllObjects()
	if err != nil {
		logrus.WithError(err).Error("failed to get all objects")
	}

	if err = d.writeFiles(objs); err != nil {
		return err
	}
	logrus.Infof("Operator will deploy %d objects, saved to folder %s", len(objs), d.outputFolder)

	if d.realClient != nil {
		controller := &storagecluster.Controller{}
		controller.SetKubernetesClient(d.realClient)
		h := migration.New(controller)
		dsObjs, err := h.GetAllDaemonSetObjects(d.cluster.Namespace)
		if err != nil {
			return err
		}

		if len(dsObjs) > 0 &&
			dsObjs[0].GetObjectKind().GroupVersionKind().Kind == "DaemonSet" &&
			dsObjs[0].GetName() == "portworx" {
			logrus.Infof("Got %d objects from k8s cluster (daemonSet install), will compare with objects installed by operator (after migration)", len(dsObjs))
			// Do not return error as it's quiet noisy, a lot of places are expected to be different between Daemonset
			// and operator install. If there is difference, warning is already logged for user to review.
			err = d.validateObjects(dsObjs, objs, h)
			if err == nil {
				logrus.Info("Operator migration dry-run finished successfully")
			} else {
				logrus.Warning("Please review the warning/error messages and/or refer to the troubleshooting guide")
			}
		} else {
			logrus.Infof("could not find DaemonSet portworx, will not dry run daemonset to operator migration")
		}
	}

	return nil
}

func (d *DryRun) writeFiles(objs []client.Object) error {
	if _, err := os.Stat(d.outputFolder); errors.Is(err, os.ErrNotExist) {
		err := os.Mkdir(d.outputFolder, os.ModePerm)
		if err != nil {
			logrus.WithError(err).Errorf("failed to create folder %s", d.outputFolder)
		}
	}

	fileAllComponents := fmt.Sprintf("%s/%s", d.outputFolder, defaultFileAllComponents)
	f, err := os.OpenFile(fileAllComponents, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		logrus.WithError(err).Errorf("failed to open file %s", fileAllComponents)
		return err
	}
	defer f.Close()

	for _, obj := range objs {
		logrus.Debugf("Operator will deploy %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
		bytes, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}

		_, err = f.WriteString(string(bytes) + "---\n")
		if err != nil {
			logrus.WithError(err).Errorf("failed to write file %s", fileAllComponents)
			return err
		}

		objFileName := fmt.Sprintf("%s/%s.%s.yaml", d.outputFolder, obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
		objFile, err := os.OpenFile(objFileName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			logrus.WithError(err).Errorf("failed to open file %s", objFileName)
			return err
		}
		defer objFile.Close()

		_, err = objFile.WriteString(string(bytes))
		if err != nil {
			logrus.WithError(err).Errorf("failed to write file %s", objFileName)
			return err
		}
	}

	return nil
}

func (d *DryRun) installAllComponents() error {
	// Create a secret for alert manager so installation can proceed
	err := d.mockClient.Create(
		context.TODO(),
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      component.AlertManagerConfigSecretName,
				Namespace: d.cluster.Namespace,
			},
		})
	if err != nil {
		logrus.WithError(err).Error("failed to create mock secret for alertManager")
	}

	funcGetDir := func() string {
		ex, err := os.Executable()
		if err != nil {
			logrus.Warningf("failed to get executable path")
			return ""
		}

		return filepath.Dir(ex) + "/configs"
	}
	pxutil.SpecsBaseDir = funcGetDir

	_, err = d.mockController.Reconcile(
		context.TODO(),
		reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      d.cluster.Name,
				Namespace: d.cluster.Namespace,
			},
		})
	if err != nil {
		return err
	}

	// Update the storage cluster after reconcile, which is updated with default values.
	err = d.mockClient.Get(context.TODO(),
		types.NamespacedName{
			Name:      d.cluster.Name,
			Namespace: d.cluster.Namespace,
		},
		d.cluster)
	if err != nil {
		return err
	}

	// We need to reconcile all components again with portworx enabled.
	// Although it's done in controller reconcile, portworx was disabled to avoid calling to portworx SDKs,
	//and many components are disabled when portworx is disabled.
	d.cluster.Annotations[constants.AnnotationDisableStorage] = "false"

	for _, comp := range component.GetAll() {
		enabled := comp.IsEnabled(d.cluster)
		if skipComponents[comp.Name()] {
			// Only log a warning if component is enabled but not supported.
			if enabled {
				logrus.Debugf("component \"%s\": dry run is not supported yet, component enabled: %t.", comp.Name(), enabled)
			}
			continue
		}

		logrus.Debugf("component \"%s\" enabled: %t", comp.Name(), enabled)
		if enabled {
			err := comp.Reconcile(d.cluster)
			if ce, ok := err.(*component.Error); ok &&
				ce.Code() == component.ErrCritical {
				return err
			} else if err != nil {
				logrus.Errorf("Failed to setup %s. %v", comp.Name(), err)
			}
		} else {
			if err := comp.Delete(d.cluster); err != nil {
				logrus.Errorf("Failed to cleanup %v. %v", comp.Name(), err)
			}
		}
	}

	return nil
}

func (d *DryRun) simulateK8sNode() error {
	if d.realClient == nil {
		node := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "mockNode",
				Labels: make(map[string]string),
			},
			Status: v1.NodeStatus{
				Allocatable: map[v1.ResourceName]resource.Quantity{
					v1.ResourcePods: resource.MustParse(strconv.Itoa(1)),
				},
			},
		}
		if err := d.mockClient.Create(context.TODO(), node); err != nil {
			return err
		}
	} else {
		nodeList := &v1.NodeList{}
		err := d.realClient.List(context.TODO(), nodeList)
		if err != nil || nodeList == nil || len(nodeList.Items) == 0 {
			return fmt.Errorf("failed to get real k8s node, err %v, nodeList %+v", err, nodeList)
		}

		for _, node := range nodeList.Items {
			d.cleanupObject(&node)
			err = d.mockClient.Create(context.TODO(), &node)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *DryRun) cleanupObject(obj client.Object) error {
	obj.SetGenerateName("")
	obj.SetUID("")
	obj.SetResourceVersion("")
	obj.SetGeneration(0)
	obj.SetSelfLink("")
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetFinalizers(nil)
	obj.SetOwnerReferences(nil)
	obj.SetClusterName("")
	obj.SetManagedFields(nil)

	return nil
}

func (d *DryRun) validateObjects(dsObjs, operatorObjs []client.Object, h *migration.Handler) error {
	// object kind, object name to object
	operatorObjMap := make(map[string]map[string]client.Object)
	for _, obj := range operatorObjs {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		if _, ok := operatorObjMap[kind]; !ok {
			operatorObjMap[kind] = make(map[string]client.Object)
		}
		operatorObjMap[kind][obj.GetName()] = obj
	}

	var lastErr error
	for _, dsObj := range dsObjs {
		kind := dsObj.GetObjectKind().GroupVersionKind().Kind
		name := dsObj.GetName()

		if skipObjectKinds[kind] {
			continue
		}

		// Map daemonset-installed object to operator-installed object
		if kind == "DaemonSet" && name == "portworx" {
			// Portworx DaemonSet is deployed as Pod by operator
			kind = "Pod"
			logrus.Info("Portworx will be deployed via Pod scheduled by operator (instead of DaemonSet)")
		} else if kind == "Deployment" && name == "px-csi-ext" {
			if m, ok := operatorObjMap["StatefulSet"]; ok {
				if _, ok := m[name]; ok {
					kind = "StatefulSet"
					logrus.Info("px-csi-ext is deployed as StatefulSet instead of Deployment by operator")
				}
			}
		} else if kind == "Deployment" && name == "prometheus-operator" {
			logrus.Info("Prometheus deployment will change from prometheus-operator to px-prometheus-operator after migration")
			name = "px-prometheus-operator"
			// Not compare deployment as it's expected to be different with helm install.
			// 1. Container name will change from prometheus-operator to px-prometheus-operator
			// 2. args is different: before-migration [--kubelet-service=kube-system/kubelet --config-reloader-image=quay.io/coreos/configmap-reload:v0.0.1], after-migration [-namespaces=kube-system --kubelet-service=kube-system/kubelet --prometheus-config-reloader=quay.io/coreos/prometheus-config-reloader:v0.36.0 --config-reloader-image=quay.io/coreos/configmap-reload:v0.0.1]
			continue
		} else if kind == "Prometheus" && name == "prometheus" {
			logrus.Info("Prometheus service name will change from prometheus to px-prometheus after migration")
			name = "px-prometheus"
		} else if kind == "Service" && name == "prometheus" {
			name = "px-prometheus"
		} else if kind == "Service" && name == "autopilot" {
			// Operator does not create autopilot service
			continue
		} else if kind == "ServiceMonitor" && name == "portworx-prometheus-sm" {
			logrus.Info("ServiceMonitor name will change from portworx-prometheus-sm to portworx after migration")
			name = "portworx"
		}

		foundOpObj := true
		if _, ok := operatorObjMap[kind]; !ok {
			foundOpObj = false
		} else if _, ok := operatorObjMap[kind][name]; !ok {
			foundOpObj = false
		}
		if !foundOpObj {
			msg := fmt.Sprintf("object %s:%s is found in daemonset install but not operator install", kind, name)
			logrus.Warning(msg)
			lastErr = fmt.Errorf(msg)
			continue
		}

		opObj := operatorObjMap[kind][name]

		var err error
		switch kind {
		// This is only for portworx pod
		case "Pod":
			err = d.deepEqualPod(&dsObj.(*appsv1.DaemonSet).Spec.Template, &v1.PodTemplateSpec{Spec: opObj.(*v1.Pod).Spec})
		case "DaemonSet":
			err = d.deepEqualPod(&dsObj.(*appsv1.DaemonSet).Spec.Template, &opObj.(*appsv1.DaemonSet).Spec.Template)
		case "Deployment":
			if name == "stork" {
				// To skip expected failure:
				//  "Containers are different: command is different: [/stork --driver=pxd --verbose --leader-elect=true --health-monitor-interval=120 --webhook-controller=false], [/stork --driver=pxd --health-monitor-interval=120 --leader-elect=true --lock-object-namespace=kube-system --verbose=true]\nenv vars is different, object \"STORK-NAMESPACE\" exists in second array but does not exist in first array.\n\n\n\n"
				dsObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
				opObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
				dsObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Env = nil
				opObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Env = nil
			} else if name == "portworx-pvc-controller" {
				// On IKS, pvc controller container will not have these two arguments after migration: leader-elect-resource-name=portworx-pvc-controller and --secure-port=9031
				//WARN[0023] command is different: before-migration [kube-controller-manager --leader-elect=true --secure-port=9031 --controllers=persistentvolume-binder,persistentvolume-expander --use-service-account-credentials=true --leader-elect-resource-lock=configmaps --leader-elect-resource-name=portworx-pvc-controller], after-migration [kube-controller-manager --leader-elect=true --controllers=persistentvolume-binder,persistentvolume-expander --use-service-account-credentials=true --leader-elect-resource-lock=configmaps]
				//WARN[0023] Containers are different: container portworx-pvc-controller-manager is different
				dsObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
				opObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
			} else if name == "autopilot" {
				// To skip expected failure:
				// "Containers are different: command is different: [/autopilot -f ./etc/config/config.yaml -log-level debug], [/autopilot --config=/etc/config/config.yaml --log-level=debug]\n\n\n"
				dsObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
				opObj.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Command = nil
			} else if name == "px-csi-ext" {
				err = d.deepEqualCSIPod(&dsObj.(*appsv1.Deployment).Spec.Template, &opObj.(*appsv1.Deployment).Spec.Template)
			} else {
				err = d.deepEqualPod(&dsObj.(*appsv1.Deployment).Spec.Template, &opObj.(*appsv1.Deployment).Spec.Template)
			}
		case "StatefulSet":
			if name == "px-csi-ext" {
				err = d.deepEqualCSIPod(&dsObj.(*appsv1.Deployment).Spec.Template, &opObj.(*appsv1.StatefulSet).Spec.Template)
			} else {
				err = d.deepEqualPod(&dsObj.(*appsv1.StatefulSet).Spec.Template, &opObj.(*appsv1.StatefulSet).Spec.Template)
			}
		case "Service":
			err = d.deepEqualService(dsObj.(*v1.Service), opObj.(*v1.Service))
		default:
			logrus.Debugf("Object %s/%s exists before and after migration, but was not compared", kind, name)
			continue
		}

		if err != nil {
			lastErr = err
			logrus.Warningf("failed to compare %s %s, %s", kind, name, err.Error())
		} else {
			logrus.Debugf("successfully compared %s %s", kind, name)
		}
	}

	return lastErr
}

func (d *DryRun) getStorageCluster(storageClusterFile string) (*corev1.StorageCluster, error) {
	if storageClusterFile != "" {
		objs, err := inttestutil.ParseSpecsWithFullPath(storageClusterFile)
		if err != nil {
			return nil, err
		}
		if len(objs) != 1 {
			return nil, fmt.Errorf("require 1 object in the config file, found objects: %+v", objs)
		}
		cluster, ok := objs[0].(*corev1.StorageCluster)
		if !ok {
			return nil, fmt.Errorf("object is not storagecluster, %+v", objs[0])
		}
		return cluster, nil
	}

	if d.realClient == nil {
		return nil, fmt.Errorf("could not load storage cluster, either pass an input file or KUBECONFIG")
	}
	nsList := &v1.NamespaceList{}
	err := d.realClient.List(context.TODO(), nsList)
	if err != nil {
		return nil, err
	}

	for _, namespace := range nsList.Items {
		clusterList := &corev1.StorageClusterList{}
		err = d.realClient.List(context.TODO(), clusterList, &client.ListOptions{
			Namespace: namespace.Name,
		})
		if err != nil {
			logrus.WithError(err).Error("failed to list StorageCluster")
		}
		if len(clusterList.Items) > 0 {
			cluster := &clusterList.Items[0]
			d.cleanupObject(cluster)
			return cluster, nil
		}
	}

	return nil, fmt.Errorf("could not find storagecluster object from k8s")
}

func (d *DryRun) getRealK8sClient(kubeconfig string) (client.Client, error) {
	var err error
	var config *rest.Config
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	d.outputFolder = defaultOutputFolder
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
		if err == nil {
			// When running inside of container, creating folder on root will get permission denied.
			d.outputFolder = "/tmp/" + d.outputFolder
			logrus.Debugf("output folder is %s", d.outputFolder)
		}
	}
	if err != nil {
		return nil, err
	}

	d.realControllerMgr, err = manager.New(config, manager.Options{})
	if err != nil {
		return nil, err
	}

	if err := apis.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add resources to the scheme: %v", err)
	}
	if err := monitoringv1.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add prometheus resources to the scheme: %v", err)
	}

	if err := cluster_v1alpha1.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := ocp_configv1.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := corev1.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add corev1 resources to the scheme: %v", err)
	}

	if err := v1.AddToScheme(d.realControllerMgr.GetScheme()); err != nil {
		logrus.Fatalf("Failed to add v1 resources to the scheme: %v", err)
	}

	start := func() {
		if err := d.realControllerMgr.Start(ctrl.SetupSignalHandler()); err != nil {
			logrus.Fatalf("Manager exited non-zero error: %v", err)
		}
	}
	go start()
	// Wait for the cache to be started.
	time.Sleep(time.Second)

	return d.realControllerMgr.GetClient(), nil
}

func (d *DryRun) getAllObjects() ([]client.Object, error) {
	var objs []client.Object

	namespace := d.cluster.Namespace
	k8sClient := d.mockClient

	cluster := d.cluster.DeepCopy()
	cluster.Annotations[constants.AnnotationDisableStorage] = "false"
	t, err := d.mockController.CreatePodTemplate(cluster, &v1.Node{}, "")
	if err != nil {
		logrus.WithError(err).Warningf("failed to get portworx pod template")
	} else {
		pod := v1.Pod{
			TypeMeta: metav1.TypeMeta{
				Kind: "Pod",
			},
			ObjectMeta: t.ObjectMeta,
			Spec:       t.Spec,
		}
		if pod.Name == "" {
			pod.Name = "portworx"
		}
		objs = append(objs, &pod)
	}

	appendObjectList(k8sClient, namespace, &v1.ServiceList{}, &objs)
	appendObjectList(k8sClient, namespace, &v1.ServiceAccountList{}, &objs)
	appendObjectList(k8sClient, namespace, &v1.SecretList{}, &objs)
	appendObjectList(k8sClient, namespace, &v1.ConfigMapList{}, &objs)
	appendObjectList(k8sClient, namespace, &v1.PodList{}, &objs)
	appendObjectList(k8sClient, namespace, &v1.PersistentVolumeClaimList{}, &objs)

	appendObjectList(k8sClient, namespace, &appsv1.DaemonSetList{}, &objs)
	appendObjectList(k8sClient, namespace, &appsv1.DeploymentList{}, &objs)
	appendObjectList(k8sClient, namespace, &appsv1.StatefulSetList{}, &objs)

	appendObjectList(k8sClient, "", &rbacv1.ClusterRoleList{}, &objs)
	appendObjectList(k8sClient, "", &rbacv1.ClusterRoleBindingList{}, &objs)
	appendObjectList(k8sClient, namespace, &rbacv1.RoleList{}, &objs)
	appendObjectList(k8sClient, namespace, &rbacv1.RoleBindingList{}, &objs)

	appendObjectList(k8sClient, "", &storagev1.StorageClassList{}, &objs)
	appendObjectList(k8sClient, "", &apiextensionsv1.CustomResourceDefinitionList{}, &objs)

	appendObjectList(k8sClient, namespace, &monitoringv1.ServiceMonitorList{}, &objs)
	appendObjectList(k8sClient, namespace, &monitoringv1.PrometheusRuleList{}, &objs)
	appendObjectList(k8sClient, namespace, &monitoringv1.PrometheusList{}, &objs)
	appendObjectList(k8sClient, namespace, &monitoringv1.AlertmanagerList{}, &objs)

	return objs, nil
}

func appendObjectList(k8sClient client.Client, namespace string, list client.ObjectList, objs *[]client.Object) error {
	err := k8sClient.List(
		context.TODO(),
		list,
		&client.ListOptions{
			Namespace: namespace,
		})
	if err != nil {
		logrus.WithError(err).Errorf("failed to list objects %v", list.GetObjectKind())
		return err
	}

	if l, ok := list.(*v1.ServiceList); ok {
		for _, o := range l.Items {
			o.Kind = "Service"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*v1.ServiceAccountList); ok {
		for _, o := range l.Items {
			o.Kind = "ServiceAccount"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*v1.SecretList); ok {
		for _, o := range l.Items {
			o.Kind = "Secret"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*v1.ConfigMapList); ok {
		for _, o := range l.Items {
			o.Kind = "ConfigMap"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*v1.PodList); ok {
		for _, o := range l.Items {
			o.Kind = "Pod"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*v1.PersistentVolumeClaimList); ok {
		for _, o := range l.Items {
			o.Kind = "PersistentVolumeClaim"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*appsv1.DaemonSetList); ok {
		for _, o := range l.Items {
			o.Kind = "DaemonSet"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*appsv1.DeploymentList); ok {
		for _, o := range l.Items {
			o.Kind = "Deployment"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*appsv1.StatefulSetList); ok {
		for _, o := range l.Items {
			o.Kind = "StatefulSet"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*rbacv1.ClusterRoleList); ok {
		for _, o := range l.Items {
			o.Kind = "ClusterRole"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*rbacv1.ClusterRoleBindingList); ok {
		for _, o := range l.Items {
			o.Kind = "ClusterRoleBinding"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*rbacv1.RoleList); ok {
		for _, o := range l.Items {
			o.Kind = "Role"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*rbacv1.RoleBindingList); ok {
		for _, o := range l.Items {
			o.Kind = "RoleBinding"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*storagev1.StorageClassList); ok {
		for _, o := range l.Items {
			o.Kind = "StorageClass"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*apiextensionsv1.CustomResourceDefinitionList); ok {
		for _, o := range l.Items {
			o.Kind = "CustomResourceDefinition"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*monitoringv1.ServiceMonitorList); ok {
		for _, o := range l.Items {
			o.Kind = "ServiceMonitor"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*monitoringv1.PrometheusRuleList); ok {
		for _, o := range l.Items {
			o.Kind = "PrometheusRule"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*monitoringv1.PrometheusList); ok {
		for _, o := range l.Items {
			o.Kind = "Prometheus"
			*objs = append(*objs, o.DeepCopy())
		}
	} else if l, ok := list.(*monitoringv1.AlertmanagerList); ok {
		for _, o := range l.Items {
			o.Kind = "Alertmanager"
			*objs = append(*objs, o.DeepCopy())
		}
	} else {
		msg := fmt.Sprintf("Unknown object kind %s", list.GetObjectKind())
		logrus.Error(msg)
		return fmt.Errorf(msg)
	}

	return nil
}
