package plugin

import (
	console "github.com/openshift/api/console/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	k8sutil "github.com/libopenstorage/operator/pkg/operator-sdk/k8sutil"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"context"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	baseDir              = "/configs/"
	nginxConfigMapFile   = "nginx-configmap.yaml"
	nginxDeploymentFile  = "nginx-deployment.yaml"
	nginxServiceFile     = "nginx-service.yaml"
	consolePluginFile    = "consoleplugin.yaml"
	pluginConfigmapFile  = "plugin-configmap.yaml"
	pluginDeploymentFile = "plugin-deployment.yaml"
	pluginServiceFile    = "plugin-service.yaml"
)

type Plugin struct {
	client   client.Client
	scheme   *runtime.Scheme
	ownerRef *metav1.OwnerReference
	ns       string
}

func NewPlugin(scheme *runtime.Scheme) *Plugin {
	operatorName, err := k8sutil.GetOperatorName()
	if err != nil {
		logrus.Error(err)
	}
	ns, err := k8sutil.GetOperatorNamespace()
	if err != nil {
		logrus.Error(err)
	}

	c, err := client.New(config.GetConfigOrDie(), client.Options{})
	if err != nil {
		logrus.Errorf("error during creating client %s", err)
		return nil
	}

	deployment := &appsv1.Deployment{}
	if err := k8s.GetDeployment(c, operatorName, ns, deployment); err != nil {
		logrus.Errorf("error during getting operator deployment  %s", err)
	}

	return &Plugin{
		client:   c,
		scheme:   scheme,
		ownerRef: metav1.NewControllerRef(deployment, v1.SchemeGroupVersion.WithKind("Deployment")),
		ns:       ns,
	}
}

func (p *Plugin) DeployPlugin() {

	//create nginx resources
	if err := p.createConfigmap(nginxConfigMapFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createDeployment(nginxDeploymentFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createService(nginxServiceFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}

	//create portworx plugin resources
	if err := p.createDeployment(pluginDeploymentFile); err != nil {
		logrus.Errorf("error during creating deployment %s ", err)
	}
	if err := p.createService(pluginServiceFile); err != nil {
		logrus.Errorf("error during creating service %s ", err)
	}
	if err := p.createConfigmap(pluginConfigmapFile); err != nil {
		logrus.Errorf("error during creating config map  %s ", err)
	}

	//create console plugin
	if err := p.createConsolePlugin(consolePluginFile); err != nil {
		logrus.Errorf("error during creating console plugin %s ", err)
	}
}

func (p *Plugin) createDeployment(filename string) error {
	deployment, err := k8s.GetDeploymentFromFile(filename, pxutil.SpecsBaseDir())
	deployment.Namespace = p.ns
	if err != nil {
		return err
	}
	logrus.Info(*deployment)
	return k8s.CreateOrUpdateDeployment(p.client, deployment, p.ownerRef)
}

func (p *Plugin) createService(filename string) error {
	service := &v1.Service{}
	service.Namespace = p.ns
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, service)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateService(p.client, service, p.ownerRef)
}

func (p *Plugin) createConfigmap(filename string) error {
	cm := &v1.ConfigMap{}
	cm.Namespace = p.ns
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, cm)
	if err != nil {
		return err
	}
	_, err = k8s.CreateOrUpdateConfigMap(p.client, cm, p.ownerRef)
	if err != nil {
		return err
	}
	return nil
}

func (p *Plugin) createConsolePlugin(filename string) error {

	if err := console.AddToScheme(p.scheme); err != nil {
		return err
	}
	cp := &console.ConsolePlugin{}
	cp.Namespace = p.ns
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, cp)
	if err != nil {
		return err
	}

	existingPlugin := &console.ConsolePlugin{}
	err = p.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      cp.Name,
			Namespace: cp.Namespace,
		},
		existingPlugin,
	)
	if errors.IsNotFound(err) {
		logrus.Infof("Creating %s Consoleplugin", existingPlugin.Name)
		return p.client.Create(context.TODO(), cp)
	} else if err != nil {
		return err
	}
	return nil
}
