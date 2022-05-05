package dryrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/golang/mock/gomock"
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

var (
	defaultOutputFile = "portworxCompnentSpecs.yaml"
)

// DryRun is a struct for portworx installation dry run
type DryRun struct {
	mockController    *storagecluster.Controller
	realControllerMgr manager.Manager
	mockControllerMgr *mock.MockManager
	// Point to real k8s cluster.
	realClient client.Client
	mockClient client.Client
	mockDriver storage.Driver
	cluster    *corev1.StorageCluster
	outputFile string
}

// Init performs required initialization
func (d *DryRun) Init(kubeconfig, outputFile, storageClusterFile string) error {
	var err error
	d.realClient, err = d.getRealK8sClient(kubeconfig)
	if err != nil {
		logrus.WithError(err).Warningf("Could not connect to k8s cluster, will run in standalone mode.")
	}

	versionClient := fakek8sclient.NewSimpleClientset()
	// TODO: read k8s version with kubeconfig
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.22.0",
	}
	// TODO: check if more SetInstances are needed, review APIs called inside of components.
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
	//	d.mockController.SetPodControl(&k8scontroller.FakePodControl{})

	d.outputFile = outputFile
	if d.outputFile == "" {
		d.outputFile = defaultOutputFile
	}

	d.cluster, err = d.getStorageCluster(storageClusterFile)
	if err != nil {
		logrus.WithError(err).Fatal("failed to get storage cluster.")
	}
	// Mock migration approved.
	d.cluster.Annotations[constants.AnnotationMigrationApproved] = "true"
	d.cluster.Annotations[constants.AnnotationPauseComponentMigration] = "false"
	d.cluster.Status.Phase = constants.PhaseMigrationInProgress
	d.cluster.Name = "mock-stc"
	// Disable storage due to the driver will talk to portworx SDK to get storage node information.
	// However dryrun is before installation so there won't be useful information.
	// It will also disable PDB.
	// We will generate portworx pod spec by calling the API directly.
	d.cluster.Annotations[constants.AnnotationDisableStorage] = "true"

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

	objs, err := d.getAllObjects()
	if err != nil {
		logrus.WithError(err).Error("failed to get all objects")
	}

	f, err := os.OpenFile(d.outputFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		logrus.WithError(err).Errorf("failed to open file %s", d.outputFile)
		return err
	}
	defer f.Close()

	for _, obj := range objs {
		logrus.Infof("Operator will deploy %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
		bytes, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}

		_, err = f.WriteString(string(bytes) + "---\n")
		if err != nil {
			logrus.WithError(err).Errorf("failed to write file %s", d.outputFile)
			return err
		}
	}

	logrus.Infof("Operator will deploy %d objects, saved to file %s", len(objs), d.outputFile)

	logrus.Infof("Start daemonSet to operator migration dry run")
	h := migration.New(&storagecluster.Controller{})
	h.SetKubernetesClient(d.realClient)
	dsObjs, err := h.GetAllDaemonSetObjects(d.cluster.Namespace)
	if err != nil {
		return err
	}

	logrus.Infof("Got %d objects from k8s cluster with daemonset install", len(dsObjs))
	err = d.validateObjs(dsObjs, objs)
	if err != nil {
		return err
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

func (d *DryRun) validateObjs(dsObjs, operatorObjs []client.Object) error {
	// object kind, object name to object
	operatorObjMap := make(map[string]map[string]client.Object)
	for _, obj := range operatorObjs {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		if _, ok := operatorObjMap[kind]; !ok {
			operatorObjMap[kind] = make(map[string]client.Object)
		}
		operatorObjMap[kind][obj.GetName()] = obj
	}

	for _, obj := range dsObjs {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		name := obj.GetName()

		switch kind {
		case "DaemonSet":
			if name == "portworx" {
				logrus.Info("Found portworx daemonset")
				//opPod := operatorObjMap["Pod"][name].(*v1.Pod)
				//t, err := d.mockController.CreatePodTemplate(d.cluster, &v1.Node{}, "")
				//handler.DeepEqualPod(&obj.(*appsv1.DaemonSet).Spec.Template, opPod.Spec)
			}
		default:
			logrus.Warningf("Object of kind %s is not validated for migration, object name: %s", kind, obj.GetName())
		}
	}

	return nil
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
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
		defaultOutputFile = "/tmp/" + defaultOutputFile
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
	// TODO: populate storage cluster defaults.
	t, err := d.mockController.CreatePodTemplate(d.cluster, &v1.Node{}, "")
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
