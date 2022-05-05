package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/mock/gomock"
	ocp_configv1 "github.com/openshift/api/config/v1"
	"github.com/portworx/sched-ops/k8s/apiextensions"
	coreops "github.com/portworx/sched-ops/k8s/core"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
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
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/apis"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	_ "github.com/libopenstorage/operator/pkg/log"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/pkg/version"
	inttestutil "github.com/libopenstorage/operator/test/integration_test/utils"
)

const (
	flagVerbose        = "verbose"
	flagStorageCluster = "storagecluster,stc"
	flagKubeConfig     = "kubeconfig"
	flagOutputFile     = "output,o"
)

var (
	defaultOutputFile = "portworxComponentSpecs.yaml"
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

func main() {
	app := cli.NewApp()
	app.Name = "portworx-operator-dryrun"
	app.Usage = "Portworx operator dry run tool"
	app.Version = version.Version
	app.Action = execute

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  flagVerbose,
			Usage: "Enable verbose logging",
		},
		cli.StringFlag{
			Name:  flagStorageCluster,
			Usage: "[Optional] File for storage cluster spec, retrieve from k8s if it's not configured",
		},
		cli.StringFlag{
			Name:  flagKubeConfig,
			Usage: "[Optional] kubeconfig file",
		},
		cli.StringFlag{
			Name:  flagOutputFile,
			Usage: "[Optional] output file to save k8s object, defaults to " + defaultOutputFile,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting: %v", err)
	}
}

func execute(c *cli.Context) {
	verbose := c.Bool(flagVerbose)
	if verbose {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	if err := dryRun(c); err != nil {
		log.WithError(err).Errorf("dryrun failed")
	}
}

func dryRun(c *cli.Context) error {
	cluster, err := getStorageCluster(c)
	if err != nil {
		return err
	}

	apiextensions.SetInstance(apiextensions.New(fakeextclient.NewSimpleClientset()))
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	mockCtrl := gomock.NewController(nil)
	defer mockCtrl.Finish()

	dryRunDriver, err := storage.Get(pxutil.DriverName)
	if err != nil {
		log.Fatalf("Error getting dry-run Storage driver %v", err)
	}

	dryRunClient := testutil.FakeK8sClient()
	if err = dryRunDriver.Init(dryRunClient, runtime.NewScheme(), record.NewFakeRecorder(100)); err != nil {
		log.Fatalf("Error initializing Storage driver for dry run %v", err)
	}

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
		enabled := comp.IsEnabled(cluster)
		if skipComponents[comp.Name()] {
			// Only log a warning if component is enabled but not supported.
			if enabled {
				log.Warningf("component \"%s\": dry run is not supported yet, component enabled: %t.", comp.Name(), enabled)
			}
			continue
		}

		log.Infof("component \"%s\" enabled: %t", comp.Name(), enabled)
		if enabled {
			err := comp.Reconcile(cluster)
			if ce, ok := err.(*component.Error); ok &&
				ce.Code() == component.ErrCritical {
				return err
			} else if err != nil {
				msg := fmt.Sprintf("Failed to setup %s. %v", comp.Name(), err)
				log.Warningf(msg)
			}
		} else {
			if err := comp.Delete(cluster); err != nil {
				msg := fmt.Sprintf("Failed to cleanup %v. %v", comp.Name(), err)
				log.Warningf(msg)
			}
		}
	}

	controller := storagecluster.Controller{Driver: dryRunDriver}
	controller.SetKubernetesClient(dryRunClient)
	objs, err := getAllObjects(controller, cluster)
	if err != nil {
		log.WithError(err).Error("failed to get all objects")
	}

	outputFile := c.String(flagOutputFile)
	if outputFile == "" {
		outputFile = defaultOutputFile
	}

	var sb strings.Builder
	for _, obj := range objs {
		log.Infof("Operator will deploy %s %s", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName())
		bytes, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}

		sb.WriteString(string(bytes))
		sb.WriteString("---\n")
	}

	f, err := os.OpenFile(outputFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		log.WithError(err).Errorf("failed to open file %s", outputFile)
		return err
	}
	defer f.Close()
	_, err = f.WriteString(sb.String())
	if err != nil {
		log.WithError(err).Errorf("failed to write file %s", outputFile)
		return err
	}

	log.Infof("wrote %d objects to file %s after dry run", len(objs), outputFile)
	return nil
}

func getStorageCluster(c *cli.Context) (*corev1.StorageCluster, error) {
	storageClusterFile := c.String(flagStorageCluster)
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

	k8sClient, err := getK8sClient(c)
	if err != nil {
		return nil, err
	}

	nsList := &v1.NamespaceList{}
	err = k8sClient.List(context.TODO(), nsList)
	if err != nil {
		return nil, err
	}

	for _, namespace := range nsList.Items {
		clusterList := &corev1.StorageClusterList{}
		err = k8sClient.List(context.TODO(), clusterList, &client.ListOptions{
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

func getK8sClient(c *cli.Context) (client.Client, error) {
	var err error
	var config *rest.Config
	kubeconfig := c.String(flagKubeConfig)
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
		// When running inside of container, writing to root will get "permission denied" error.
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

func getAllObjects(controller storagecluster.Controller, cluster *corev1.StorageCluster) ([]client.Object, error) {
	var objs []client.Object

	k8sClient := controller.GetKubernetesClient()
	namespace := cluster.Namespace
	// TODO: populate storage cluster defaults.
	t, err := controller.CreatePodTemplate(cluster, &v1.Node{}, "")
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
