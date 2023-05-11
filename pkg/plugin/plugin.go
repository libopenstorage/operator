package plugin

import (
	console "github.com/openshift/api/console/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"context"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	baseDir                       = "/configs/"
	nginxConfigMapFile            = "nginx-configmap.yaml"
	nginxDeploymentFile           = "nginx-deployment.yaml"
	nginxServiceFile              = "nginx-service.yaml"
	consolePluginFile             = "consoleplugin.yaml"
	patchConsoleJobFile           = "patch-consoles-job.yaml"
	patcherClusterRoleFile        = "patcher-clusterrole.yaml"
	patcherClusterRoleBindingFile = "patcher-clusterrolebinding.yaml"
	patcherServiceAccountFile     = "patcher-serviceaccount.yaml"
	pluginConfigmapFile           = "plugin-configmap.yaml"
	pluginDeploymentFile          = "plugin-deployment.yaml"
	pluginServiceFile             = "plugin-service.yaml"
	pluginServiceAccountFile      = "plugin-serviceaccount.yaml"
)

type Plugin struct {
	client   client.Client
	scheme   *runtime.Scheme
	ownerRef *metav1.OwnerReference
}

func NewPlugin(scheme *runtime.Scheme) *Plugin {

	c, err := client.New(config.GetConfigOrDie(), client.Options{})
	if err != nil {
		logrus.Errorf("error during creating client %s", err)
		return nil
	}

	deployment := &appsv1.Deployment{}
	if err := k8s.GetDeployment(c, "portworx-operator", "openshift-operators", deployment); err != nil {
		logrus.Errorf("error during getting operator deployment  %s", err)
	}

	return &Plugin{
		client:   c,
		scheme:   scheme,
		ownerRef: metav1.NewControllerRef(deployment, v1.SchemeGroupVersion.WithKind("Deployment")),
	}
}

func (p *Plugin) DeployPlugin() {

	//create nginx services
	if err := p.createConfigmap(nginxConfigMapFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createDeployment(nginxDeploymentFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createService(nginxServiceFile); err != nil {
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}

	//create plugin
	if err := p.createDeployment(pluginDeploymentFile); err != nil {
		logrus.Errorf("error during creating deployment %s ", err)
	}
	if err := p.createService(pluginServiceFile); err != nil {
		logrus.Errorf("error during creating service %s ", err)
	}
	if err := p.createConfigmap(pluginConfigmapFile); err != nil {
		logrus.Errorf("error during creating config map  %s ", err)
	}
	if err := p.createServiceAccount(pluginServiceAccountFile); err != nil {
		logrus.Errorf("error during creating service acount %s ", err)
	}
	if err := p.createServiceAccount(patcherServiceAccountFile); err != nil {
		logrus.Errorf("error during creating service acount %s ", err)
	}
	if err := p.createClusterRole(patcherClusterRoleFile); err != nil {
		logrus.Errorf("error during creating cluster role %s ", err)
	}
	if err := p.createClusterRoleBinding(patcherClusterRoleBindingFile); err != nil {
		logrus.Errorf("error during creating cluster role binding %s ", err)
	}
	if err := p.createJob(patchConsoleJobFile); err != nil {
		logrus.Errorf("error during creating job %s ", err)
	}
	if err := p.createConsolePlugin(consolePluginFile); err != nil {
		logrus.Errorf("error during creating console plugin %s ", err)
	}
}

func (p *Plugin) createDeployment(filename string) error {
	deployment, err := k8s.GetDeploymentFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateDeployment(p.client, deployment, p.ownerRef)
}

func (p *Plugin) createClusterRole(filename string) error {
	roleObj := &rbacv1.ClusterRole{}
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, roleObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRole(p.client, roleObj)
}

func (p *Plugin) createClusterRoleBinding(filename string) error {
	roleBindingObj := &rbacv1.ClusterRoleBinding{}
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, roleBindingObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRoleBinding(p.client, roleBindingObj)
}

func (p *Plugin) createService(filename string) error {
	service := &v1.Service{}
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, service)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateService(p.client, service, p.ownerRef)
}

func (p *Plugin) createServiceAccount(filename string) error {
	serviceAccount := &v1.ServiceAccount{}
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, serviceAccount)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateServiceAccount(p.client, serviceAccount, p.ownerRef)
}

func (p *Plugin) createConfigmap(filename string) error {
	cm := &v1.ConfigMap{}
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

func (p *Plugin) createJob(filename string) error {

	job := &batchv1.Job{}
	err := k8s.ParseObjectFromFile(baseDir+filename, p.scheme, job)
	if err != nil {
		return err
	}

	existingJob := &batchv1.Job{}
	err = p.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      job.Name,
			Namespace: job.Namespace,
		},
		existingJob,
	)
	if errors.IsNotFound(err) {
		logrus.Infof("Creating %s Job", job.Name)
		return p.client.Create(context.TODO(), job)
	} else if err != nil {
		return err
	}
	return nil
}
