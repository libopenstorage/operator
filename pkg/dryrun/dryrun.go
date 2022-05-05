package dryrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang/mock/gomock"
	ocp_configv1 "github.com/openshift/api/config/v1"
	"github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	log "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	fakeextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/pkg/apis/deprecated/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/yaml"

	"github.com/libopenstorage/operator/drivers/storage"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/apis"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/migration"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	inttestutil "github.com/libopenstorage/operator/test/integration_test/utils"
)

var (
	defaultOutputFile = "portworxCompnentSpecs.yaml"
	skipComponents    = map[string]bool{
		// CRD component registration is stuck at CRD validation as fake client does not set status of CRD.
		// Potential fixes:
		//   1. remove the validation -- need more tests to see if it's safe to remove it.
		//   2. create a thread to monitor the CRD and update status for dry run.
		component.PortworxCRDComponentName: true,
		// Disruption budget component needs to talk to portworx SDK, it also needs real k8s cluster to set correct value.
		component.DisruptionBudgetComponentName: true,
		// Alert manager requires a secret to be deployed first.
		// We may read the secret from real k8s cluster or generate a secret for dry run.
		component.AlertManagerComponentName: true,
	}
)

// DryRun is a struct for portworx installation dry run
type DryRun struct {
	mockController *storagecluster.Controller
	// Point to real k8s client if kubeconfig is provided, or running inside of container.
	realClient client.Client
	mockClient client.Client
	mockDriver storage.Driver
	cluster    *corev1.StorageCluster
	outputFile string
}

// Init performs required initialization
func (d *DryRun) Init(kubeconfig, outputFile, storageClusterFile string) error {
	var err error
	d.realClient, err = d.getK8sClient(kubeconfig)
	if err != nil {
		log.WithError(err).Warningf("Could not connect to k8s cluster, will run in standalone mode.")
	}

	d.cluster, err = d.getStorageCluster(storageClusterFile)
	if err != nil {
		log.WithError(err).Fatal("failed to get storage cluster.")
	}

	apiextensions.SetInstance(apiextensions.New(fakeextclient.NewSimpleClientset()))
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(nil)
	defer mockCtrl.Finish()

	d.mockDriver, err = storage.Get(pxutil.DriverName)
	if err != nil {
		log.WithError(err).Fatalf("failed to get mock driver")
	}

	d.mockClient = testutil.FakeK8sClient()
	if err = d.mockDriver.Init(d.mockClient, runtime.NewScheme(), record.NewFakeRecorder(100)); err != nil {
		log.WithError(err).Fatalf("failed to initialize mock driver")
	}

	d.mockController = &storagecluster.Controller{Driver: d.mockDriver}
	d.mockController.SetKubernetesClient(d.mockClient)

	d.outputFile = outputFile
	if d.outputFile == "" {
		d.outputFile = defaultOutputFile
	}

	return nil
}

// Execute dry run for portworx installation
func (d *DryRun) Execute() error {
	funcGetDir := func() string {
		ex, err := os.Executable()
		if err != nil {
			log.Warningf("failed to get executable path")
			return ""
		}

		return filepath.Dir(ex) + "/configs"
	}
	pxutil.SpecsBaseDir = funcGetDir

	for _, comp := range component.GetAll() {
		enabled := comp.IsEnabled(d.cluster)
		if skipComponents[comp.Name()] {
			// Only log a warning if component is enabled but not supported.
			if enabled {
				log.Warningf("component \"%s\": dry run is not supported yet, component enabled: %t.", comp.Name(), enabled)
			}
			continue
		}

		log.Infof("component \"%s\" enabled: %t", comp.Name(), enabled)
		if enabled {
			err := comp.Reconcile(d.cluster)
			if ce, ok := err.(*component.Error); ok &&
				ce.Code() == component.ErrCritical {
				return err
			} else if err != nil {
				msg := fmt.Sprintf("Failed to setup %s. %v", comp.Name(), err)
				log.Warningf(msg)
			}
		} else {
			if err := comp.Delete(d.cluster); err != nil {
				msg := fmt.Sprintf("Failed to cleanup %v. %v", comp.Name(), err)
				log.Warningf(msg)
			}
		}
	}

	objs, err := d.getAllObjects()
	if err != nil {
		log.WithError(err).Error("failed to get all objects")
	}

	f, err := os.OpenFile(d.outputFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		log.WithError(err).Errorf("failed to open file %s", d.outputFile)
		return err
	}
	defer f.Close()

	for _, obj := range objs {
		log.Infof("Operator will deploy %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
		bytes, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}

		_, err = f.WriteString(string(bytes) + "---\n")
		if err != nil {
			log.WithError(err).Errorf("failed to write file %s", d.outputFile)
			return err
		}
	}

	log.Infof("Operator will deploy %d objects, saved to file %s", len(objs), d.outputFile)

	log.Infof("start daemonSet to operator migration dry run")
	h := migration.New(d.mockController)
	dsObjs, err := h.GetAllDaemonSetObjects(d.cluster.Namespace)
	if err != nil {
		return err
	}

	err = d.validateObjs(dsObjs, objs)
	if err != nil {
		return err
	}

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
				log.Info("Found portworx daemonset")
				//opPod := operatorObjMap["Pod"][name].(*v1.Pod)
				//t, err := d.mockController.CreatePodTemplate(d.cluster, &v1.Node{}, "")
				//handler.DeepEqualPod(&obj.(*appsv1.DaemonSet).Spec.Template, opPod.Spec)
			}
		default:
			log.Warningf("Object kind %s is not validated for migration", kind)
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
			log.WithError(err).Error("failed to list StorageCluster")
		}
		if len(clusterList.Items) > 0 {
			return &clusterList.Items[0], nil
		}
	}

	return nil, fmt.Errorf("could not find storagecluster object from k8s")
}

func (d *DryRun) getK8sClient(kubeconfig string) (client.Client, error) {
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

	mgr, err := manager.New(config, manager.Options{})
	if err != nil {
		return nil, err
	}

	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add resources to the scheme: %v", err)
	}
	if err := monitoringv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add prometheus resources to the scheme: %v", err)
	}

	if err := cluster_v1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := ocp_configv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := corev1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add corev1 resources to the scheme: %v", err)
	}

	if err := v1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add v1 resources to the scheme: %v", err)
	}

	start := func() {
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			log.Fatalf("Manager exited non-zero error: %v", err)
		}
	}
	go start()
	time.Sleep(time.Second)

	return mgr.GetClient(), nil
}

func (d *DryRun) getAllObjects() ([]client.Object, error) {
	var objs []client.Object

	namespace := d.cluster.Namespace
	k8sClient := d.mockClient
	// TODO: populate storage cluster defaults.
	t, err := d.mockController.CreatePodTemplate(d.cluster, &v1.Node{}, "")
	if err != nil {
		log.WithError(err).Warningf("failed to get portworx pod template")
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
		log.WithError(err).Errorf("failed to list objects %v", list.GetObjectKind())
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
		log.Error(msg)
		return fmt.Errorf(msg)
	}

	return nil
}
