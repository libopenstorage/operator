package plugin

import (
	"errors"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/runtime"
)

/*
const (
	pluginConfigMapFile           = "configmap.yaml"
	nginxConfigMapFile            = "nginx-configmap.yaml"
	consolePluginFile             = "consoleplugin.yaml"
	pluginDeploymentFile          = "deployment.yaml"
	nginxDeploymentFile           = "nginx-deployment.yaml"
	patchConsoleJobFile           = "patch-consoles-job.yaml"
	patcherClusterRoleFile        = "patcher-clusterrole.yaml"
	patcherClusterRoleBindingFile = "patcher-clusterrolebinding.yaml"
	pluginServiceAccountFile      = "patcher-serviceaccount.yaml"
)
*/

type Plugin struct {
	client client.Client
	scheme *runtime.Scheme
}

func NewPlugin(client client.Client, scheme *runtime.Scheme) *Plugin {
	return &Plugin{
		client: client,
		scheme: scheme}
}

func (p *Plugin) DeployPlugin() {
	if err := p.createDeployment(); err != nil {
		logrus.Errorf("error during creating deployment %s ", err)
	}
	if err := p.createClusterRole(); err != nil {
		logrus.Errorf("error during creating cluster role %s ", err)
	}
	if err := p.createClusterRoleBinding(); err != nil {
		logrus.Errorf("error during creating cluster role binding %s ", err)
	}
	if err := p.createService(); err != nil {
		logrus.Errorf("error during creating service %s ", err)
	}
	if err := p.createServiceAccount(); err != nil {
		logrus.Errorf("error during creating service acount %s ", err)
	}
	if err := p.createConfigmap(); err != nil {
		logrus.Errorf("error during creating config map  %s ", err)
	}
}

func (p *Plugin) createDeployment() error {
	deployment, err := k8s.GetDeploymentFromFile("deployment.yaml", pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateDeployment(p.client, deployment, nil)
}

func (p *Plugin) createClusterRole() error {
	roleObj := &rbacv1.ClusterRole{}
	err := k8s.ParseObjectFromFile("/configs/patcher-clusterrole.yaml", p.scheme, roleObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRole(p.client, roleObj)
}

func (p *Plugin) createClusterRoleBinding() error {
	roleBindingObj := &rbacv1.ClusterRoleBinding{}
	err := k8s.ParseObjectFromFile("/configs/patcher-clusterrolebinding.yaml", p.scheme, roleBindingObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRoleBinding(p.client, roleBindingObj)
}

func (p *Plugin) createService() error {
	service := &v1.Service{}
	err := k8s.ParseObjectFromFile("/configs/service.yaml", p.scheme, service)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateService(p.client, service, nil)
}

func (p *Plugin) createServiceAccount() error {
	serviceAccount := &v1.ServiceAccount{}
	err := k8s.ParseObjectFromFile("/configs/serviceaccount.yaml", p.scheme, serviceAccount)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateServiceAccount(p.client, serviceAccount, nil)
}

func (p *Plugin) createConfigmap() error {
	cm := &v1.ConfigMap{}
	err := k8s.ParseObjectFromFile("/configs/configmap.yaml", p.scheme, cm)
	if err != nil {
		return err
	}
	created, err := k8s.CreateOrUpdateConfigMap(p.client, cm, nil)
	if err != nil {
		return err
	}
	if !created {
		logrus.Info("Configmap is not created")
		return errors.New("configmap is not created")
	}
	return nil

}
