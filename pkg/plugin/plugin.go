package plugin

import (
	"errors"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

type Plugin struct {
	Client client.Client
}

func NewPlugin(client client.Client) *Plugin {
	return &Plugin{Client: client}
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
}

func (p *Plugin) createDeployment() error {
	deployment, err := k8s.GetDeploymentFromFile("deployment.yaml", pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateDeployment(p.Client, deployment, nil)
}

func (p *Plugin) createClusterRole() error {
	roleObj := &rbacv1.ClusterRole{}
	err := k8s.ParseObjectFromFile("patcher-clusterrole.yaml", nil, roleObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRole(p.Client, roleObj)
}

func (p *Plugin) createClusterRoleBinding() error {
	roleBindingObj := &rbacv1.ClusterRoleBinding{}
	err := k8s.ParseObjectFromFile("patcher-clusterrole-binding.yaml", nil, roleBindingObj)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateClusterRoleBinding(p.Client, roleBindingObj)
}

func (p *Plugin) createService() error {
	service := &v1.Service{}
	err := k8s.ParseObjectFromFile("service.yaml", nil, service)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateService(p.Client, service, nil)
}

func (p *Plugin) createServiceAccount(fileName string) error {
	serviceAccount := &v1.ServiceAccount{}
	err := k8s.ParseObjectFromFile(fileName, nil, serviceAccount)
	if err != nil {
		return err
	}
	return k8s.CreateOrUpdateServiceAccount(p.Client, serviceAccount, nil)
}

func (p *Plugin) createConfigmap(fileName string) error {
	cm := &v1.ConfigMap{}
	err := k8s.ParseObjectFromFile(fileName, nil, cm)
	if err != nil {
		return err
	}
	created, err := k8s.CreateOrUpdateConfigMap(p.Client, cm, nil)
	if err != nil {
		return err
	}
	if !created {
		logrus.Info("Configmap is not created")
		return errors.New("Configmap is not created")
	}
	return nil

}
