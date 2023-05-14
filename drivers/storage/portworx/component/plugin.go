package component

import (
	"context"
	commonerrors "errors"
	"fmt"
	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/operator-sdk/k8sutil"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	ocpconfig "github.com/openshift/api/config/v1"
	console "github.com/openshift/api/console/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"strings"
)

const (
	PluginComponentName       = "Portworx Plugin"
	PluginName                = "portworx"
	PluginDeloymentName       = "portworx-plugin"
	PluginConfigMapName       = "portworx-plugin"
	PluginServiceName         = "portworx-plugin"
	NginxDeploymentName       = "portworx-console-proxy"
	NginxConfigMapName        = "nginx-conf"
	NginxServiceName          = "portworx-console-proxy"
	OpenshiftClusterName      = "openshift-apiserver"
	OpenshiftSupportedVersion = "4.12"
	BaseDir                   = "/configs/"
	nginxConfigMapFileName    = "nginx-configmap.yaml"
	nginxDeploymentFileName   = "nginx-deployment.yaml"
	nginxServiceFileName      = "nginx-service.yaml"
	consolePluginFileName     = "consoleplugin.yaml"
	pluginConfigmapFileName   = "plugin-configmap.yaml"
	pluginDeploymentFileName  = "plugin-deployment.yaml"
	pluginServiceFileName     = "plugin-service.yaml"
)

type plugin struct {
	client     client.Client
	scheme     *runtime.Scheme
	operatorNs string
}

func (p *plugin) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
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
	}

	deployment := &appsv1.Deployment{}
	if err := k8s.GetDeployment(c, operatorName, ns, deployment); err != nil {
		logrus.Errorf("error during getting operator deployment  %s", err)
	}

	p.client = k8sClient
	p.scheme = scheme
	p.operatorNs = ns

}

func (c *plugin) Name() string {
	return PluginComponentName
}

func (c *plugin) Priority() int32 {
	return DefaultComponentPriority
}

func (c *plugin) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (p *plugin) IsEnabled(cluster *corev1.StorageCluster) bool {
	operator := &ocpconfig.ClusterOperator{}
	err := p.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name: OpenshiftClusterName,
		},
		operator,
	)

	if errors.IsNotFound(err) {
		return false
	}

	for _, v := range operator.Status.Versions {
		if v.Name == OpenshiftClusterName && isVersionGreaterOrEqual(v.Version, OpenshiftSupportedVersion) {
			return true
		}
	}

	return false
}

func (p *plugin) Reconcile(cluster *corev1.StorageCluster) error {

	ownRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	var errList []string
	//create nginx resources
	if err := p.createConfigmap(nginxConfigMapFileName, ownRef, cluster.Namespace); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createDeployment(nginxDeploymentFileName, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}
	if err := p.createService(nginxServiceFileName, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating nginx configmap %s ", err)
	}

	//create portworx plugin resources
	if err := p.createDeployment(pluginDeploymentFileName, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating deployment %s ", err)
	}
	if err := p.createService(pluginServiceFileName, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating service %s ", err)
	}
	if err := p.createConfigmap(pluginConfigmapFileName, ownRef, ""); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating config map  %s ", err)
	}

	//create console plugin
	if err := p.createConsolePlugin(consolePluginFileName, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating console plugin %s ", err)
	}

	if len(errList) > 0 {
		return commonerrors.New(strings.Join(errList, " , "))
	}
	return nil
}

func (p *plugin) Delete(cluster *corev1.StorageCluster) error {
	var errList []string
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	//delete plugin configs
	if err := k8s.DeleteDeployment(p.client, PluginDeloymentName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteConfigMap(p.client, PluginConfigMapName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteService(p.client, PluginServiceName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}

	//delete nginx configs
	if err := k8s.DeleteDeployment(p.client, NginxDeploymentName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteConfigMap(p.client, NginxConfigMapName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteService(p.client, NginxServiceName, p.operatorNs, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}

	//delete console plugin
	if err := p.deleteConsolePlugin(PluginName, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if len(errList) > 0 {
		return commonerrors.New(strings.Join(errList, " , "))
	}
	return nil
}

func (c *plugin) MarkDeleted() {}

// RegisterPortworxPluginComponent registers the PortworxPlugin component
func RegisterPortworxPluginComponent() {
	Register(PluginComponentName, &plugin{})
}

func init() {
	RegisterPortworxPluginComponent()
}

func (p *plugin) createDeployment(filename string, ownerRef *metav1.OwnerReference) error {
	deployment, err := k8s.GetDeploymentFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	deployment.Namespace = p.operatorNs
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	return k8s.CreateOrUpdateDeployment(p.client, deployment, ownerRef)
}

func (p *plugin) createService(filename string, ownerRef *metav1.OwnerReference) error {
	service := &v1.Service{}
	err := k8s.ParseObjectFromFile(BaseDir+filename, p.scheme, service)
	if err != nil {
		return err
	}
	service.Namespace = p.operatorNs
	service.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	return k8s.CreateOrUpdateService(p.client, service, ownerRef)
}

func (p *plugin) createConfigmap(filename string, ownerRef *metav1.OwnerReference, stcNamespace string) error {
	cm := &v1.ConfigMap{}
	err := k8s.ParseObjectFromFile(BaseDir+filename, p.scheme, cm)
	if err != nil {
		return err
	}
	cm.Namespace = p.operatorNs
	cm.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	updateDataIfNginxConfigMap(cm, p.operatorNs, stcNamespace)

	_, err = k8s.CreateOrUpdateConfigMap(p.client, cm, ownerRef)
	return err
}

func (p *plugin) createConsolePlugin(filename string, ownerRef *metav1.OwnerReference) error {

	if err := console.AddToScheme(p.scheme); err != nil {
		return err
	}

	cp := &console.ConsolePlugin{}
	if err := k8s.ParseObjectFromFile(BaseDir+filename, p.scheme, cp); err != nil {
		return err
	}

	cp.Namespace = p.operatorNs
	cp.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	if cp.Spec.Backend.Service != nil {
		cp.Spec.Backend.Service.Namespace = p.operatorNs
	}

	if len(cp.Spec.Proxy) > 0 && cp.Spec.Proxy[0].Endpoint.Service != nil {
		cp.Spec.Proxy[0].Endpoint.Service.Namespace = p.operatorNs
	}

	existingPlugin := &console.ConsolePlugin{}
	err := p.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      cp.Name,
			Namespace: cp.Namespace,
		},
		existingPlugin,
	)

	if errors.IsNotFound(err) {
		logrus.Infof("Creating %s Consoleplugin", cp.Name)
		return p.client.Create(context.TODO(), cp)
	} else if err != nil {
		return err
	}
	return nil
}

func (p *plugin) deleteConsolePlugin(name string, owners ...metav1.OwnerReference) error {
	if err := console.AddToScheme(p.scheme); err != nil {
		return err
	}

	resource := types.NamespacedName{
		Name:      name,
		Namespace: p.operatorNs,
	}

	consolePlugin := &console.ConsolePlugin{}
	err := p.client.Get(context.TODO(), resource, consolePlugin)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	newOwners := k8s.RemoveOwners(consolePlugin.OwnerReferences, owners)

	// Do not delete the object if it does not have the owner that was passed;
	// even if the object has no owner
	if (len(consolePlugin.OwnerReferences) == 0 && len(owners) > 0) ||
		(len(consolePlugin.OwnerReferences) > 0 && len(consolePlugin.OwnerReferences) == len(newOwners)) {
		logrus.Infof("Cannot delete consolePlugin %s/%s as it is not owned",
			p.operatorNs, name)
		return nil
	}

	if len(newOwners) == 0 {
		logrus.Infof("Deleting %s/%s consolePlugin", p.operatorNs, name)
		return p.client.Delete(context.TODO(), consolePlugin)
	}
	consolePlugin.OwnerReferences = newOwners
	logrus.Infof("Disowning %s/%s consolePlugin", p.operatorNs, name)
	return p.client.Update(context.TODO(), consolePlugin)
}

func updateDataIfNginxConfigMap(cm *v1.ConfigMap, operaterNs, pxNs string) {
	if cm.Name == NginxConfigMapName {
		cm.Data = map[string]string{
			"nginx.conf": `pid /tmp/nginx.pid;
    events {
      worker_connections 1024;
    }
    http {
      server {
        listen 8080;
          server_name portworx-console-proxy.` + operaterNs + `.svc.cluster.local;
        location / {
          proxy_pass http://portworx-api.` + pxNs + `.svc.cluster.local:9021;
        }
      }
      server {
        listen 8443 ssl;
        server_name portworx-console-proxy.` + operaterNs + `.svc.cluster.local;
        ssl_certificate /etc/nginx/certs/tls.crt;
        ssl_certificate_key /etc/nginx/certs/tls.key;
        location / {
          proxy_pass http://portworx-api.` + pxNs + `.svc.cluster.local:9021;
        }
      }
    }`,
		}
	}
}

func isVersionGreaterOrEqual(version string, targetVersion string) bool {
	// Split the version strings into individual parts
	versionParts := strings.Split(version, ".")
	targetVersionParts := strings.Split(targetVersion, ".")

	// Iterate through each part of the version and compare them
	for i := 0; i < len(versionParts) && i < len(targetVersionParts); i++ {
		versionPart := parseVersionPart(versionParts[i])
		targetVersionPart := parseVersionPart(targetVersionParts[i])

		if versionPart > targetVersionPart {
			return true
		} else if versionPart < targetVersionPart {
			return false
		}
	}

	// If all parts are equal so far, check if the version has more parts remaining
	return len(versionParts) >= len(targetVersionParts)
}

func parseVersionPart(versionPart string) int {
	var versionPartInt int
	_, err := fmt.Sscanf(versionPart, "%d", &versionPartInt)
	if err != nil {
		// If the part cannot be parsed as an integer, treat it as 0
		return 0
	}
	return versionPartInt
}
