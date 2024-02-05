package component

import (
	"context"
	commonerrors "errors"
	"strings"

	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"

	version "github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	ocpconfig "github.com/openshift/api/config/v1"
	consolev1 "github.com/openshift/api/console/v1"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PluginComponentName       = "Portworx Plugin"
	PluginName                = "portworx"
	PluginDeploymentName      = "px-plugin"
	PluginConfigMapName       = "px-plugin"
	PluginServiceName         = "px-plugin"
	NginxDeploymentName       = "px-plugin-proxy"
	NginxConfigMapName        = "px-plugin-proxy-conf"
	NginxServiceName          = "px-plugin-proxy"
	ClusterOperatorVersion    = "config.openshift.io/v1"
	ClusterOperatorKind       = "ClusterOperator"
	OpenshiftAPIServer        = "openshift-apiserver"
	OpenshiftSupportedVersion = "4.12"
	nginxConfigMapFileName    = "nginx-configmap.yaml"
	nginxDeploymentFileName   = "nginx-deployment.yaml"
	nginxServiceFileName      = "nginx-service.yaml"
	consolePluginFileName     = "consoleplugin.yaml"
	pluginConfigmapFileName   = "plugin-configmap.yaml"
	pluginDeploymentFileName  = "plugin-deployment.yaml"
	pluginServiceFileName     = "plugin-service.yaml"
)

type plugin struct {
	client                    client.Client
	scheme                    *runtime.Scheme
	isPluginDeploymentCreated bool
	isProxyDeploymentCreated  bool
	isPluginSupported         *bool
}

func (p *plugin) Initialize(
	k8sClient client.Client,
	_ version.Version,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,
) {
	p.client = k8sClient
	p.scheme = scheme
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
	if p.isPluginSupported != nil {
		return *p.isPluginSupported
	} else {
		gvk := schema.GroupVersionKind{
			Kind:    ClusterOperatorKind,
			Version: ClusterOperatorVersion,
		}
		// CHECKME: Move this check to pxutil.IsOpenshift()
		exists, err := coreops.Instance().ResourceExists(gvk)
		if err != nil {
			logrus.Error(err)
			return false
		}

		if exists {
			operator := &ocpconfig.ClusterOperator{}
			err := p.client.Get(
				context.TODO(),
				types.NamespacedName{
					Name: OpenshiftAPIServer,
				},
				operator,
			)

			if err != nil {
				logrus.Error(err)
				if errors.IsNotFound(err) {
					p.isPluginSupported = boolPtr(false)
				}
				return false
			}

			for _, v := range operator.Status.Versions {
				if v.Name == OpenshiftAPIServer && isVersionSupported(v.Version, OpenshiftSupportedVersion) {
					p.isPluginSupported = boolPtr(true)
					return true
				}
			}
		} else {
			p.isPluginSupported = boolPtr(false)
			return false
		}
	}

	return false
}

func (p *plugin) Reconcile(cluster *corev1.StorageCluster) error {

	ownRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	var errList []string
	// create nginx resources
	if err := p.createConfigmap(nginxConfigMapFileName, cluster.Namespace, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s configmap %s ", NginxConfigMapName, err)
	}
	if err := p.createDeployment(nginxDeploymentFileName, NginxDeploymentName, ownRef, cluster); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s deployment %s ", NginxDeploymentName, err)
	}
	if err := p.createService(nginxServiceFileName, cluster.Namespace, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s service %s ", NginxServiceName, err)
	}

	// create portworx plugin resources
	if err := p.createDeployment(pluginDeploymentFileName, PluginDeploymentName, ownRef, cluster); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s deployment %s ", PluginDeploymentName, err)
	}
	if err := p.createService(pluginServiceFileName, cluster.Namespace, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s service %s ", PluginServiceName, err)
	}
	if err := p.createConfigmap(pluginConfigmapFileName, cluster.Namespace, ownRef); err != nil {
		errList = append(errList, err.Error())
		logrus.Errorf("error during creating %s config map  %s ", PluginConfigMapName, err)
	}

	// create console plugin
	if err := p.createConsolePlugin(consolePluginFileName, cluster.Namespace, ownRef); err != nil {
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

	// delete plugin configs
	if err := k8s.DeleteDeployment(p.client, PluginDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteConfigMap(p.client, PluginConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteService(p.client, PluginServiceName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}

	// delete nginx configs
	if err := k8s.DeleteDeployment(p.client, NginxDeploymentName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteConfigMap(p.client, NginxConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if err := k8s.DeleteService(p.client, NginxServiceName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}

	// delete console plugin
	if err := k8s.DeleteConsolePlugin(p.client, PluginName, cluster.Namespace, *ownerRef); err != nil {
		errList = append(errList, err.Error())
	}
	if len(errList) > 0 {
		return commonerrors.New(strings.Join(errList, " , "))
	}
	return nil
}

func (p *plugin) MarkDeleted() {
	p.isPluginDeploymentCreated = false
	p.isProxyDeploymentCreated = false
	p.isPluginSupported = boolPtr(false)
}

// RegisterPortworxPluginComponent registers the PortworxPlugin component
func RegisterPortworxPluginComponent() {
	Register(PluginComponentName, &plugin{})
}

func init() {
	RegisterPortworxPluginComponent()
}

func (p *plugin) createDeployment(filename, deploymentName string, ownerRef *metav1.OwnerReference, cluster *corev1.StorageCluster) error {
	deployment, err := k8s.GetDeploymentFromFile(filename, pxutil.SpecsBaseDir())
	if err != nil {
		return err
	}
	deployment.Namespace = cluster.Namespace
	deployment.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	deployment.Spec.Template.ObjectMeta = k8s.AddManagedByOperatorLabel(deployment.Spec.Template.ObjectMeta)

	existingDeployment := &appsv1.Deployment{}
	getErr := p.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      deploymentName,
			Namespace: cluster.Namespace,
		},
		existingDeployment,
	)
	if getErr != nil && !errors.IsNotFound(getErr) {
		return getErr
	}

	pxutil.ApplyStorageClusterSettingsToPodSpec(cluster, &deployment.Spec.Template.Spec)

	if deployment.Name == PluginDeploymentName {
		deployment.Spec.Template.Spec.Containers[0].Image = getDesiredPluginImage(cluster)
		deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy = v1.PullAlways
	}
	if deployment.Name == NginxDeploymentName {
		deployment.Spec.Template.Spec.Containers[0].Image = getDesiredPluginProxyImage(cluster)
	}

	equal, _ := util.DeepEqualPodTemplate(&deployment.Spec.Template, &existingDeployment.Spec.Template)

	if (!p.isPluginDeploymentCreated && deployment.Name == PluginDeploymentName) ||
		(!p.isProxyDeploymentCreated && deployment.Name == NginxDeploymentName) ||
		!equal {
		if err := k8s.CreateOrUpdateDeployment(p.client, deployment, ownerRef); err != nil {
			return err
		}
	}
	if deployment.Name == NginxDeploymentName {
		p.isProxyDeploymentCreated = true
	}
	if deployment.Name == PluginDeploymentName {
		p.isPluginDeploymentCreated = true
	}

	return nil
}

func (p *plugin) createService(filename, storageNs string, ownerRef *metav1.OwnerReference) error {
	service := &v1.Service{}
	file := pxutil.SpecsBaseDir() + "/" + filename
	err := k8s.ParseObjectFromFile(file, p.scheme, service)
	if err != nil {
		return err
	}
	service.Namespace = storageNs
	service.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	return k8s.CreateOrUpdateService(p.client, service, ownerRef)
}

func (p *plugin) createConfigmap(filename, storageNs string, ownerRef *metav1.OwnerReference) error {
	cm := &v1.ConfigMap{}
	file := pxutil.SpecsBaseDir() + "/" + filename
	err := k8s.ParseObjectFromFile(file, p.scheme, cm)
	if err != nil {
		return err
	}
	cm.Namespace = storageNs
	cm.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	updateDataIfNginxConfigMap(cm, storageNs)

	_, err = k8s.CreateOrUpdateConfigMap(p.client, cm, ownerRef)
	return err
}

func (p *plugin) createConsolePlugin(filename, storageNs string, ownerRef *metav1.OwnerReference) error {

	cp := &consolev1.ConsolePlugin{}
	file := pxutil.SpecsBaseDir() + "/" + filename
	if err := k8s.ParseObjectFromFile(file, p.scheme, cp); err != nil {
		return err
	}

	cp.Namespace = storageNs
	cp.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	if cp.Spec.Backend.Service != nil {
		cp.Spec.Backend.Service.Namespace = storageNs
	}

	if len(cp.Spec.Proxy) > 0 && cp.Spec.Proxy[0].Endpoint.Service != nil {
		cp.Spec.Proxy[0].Endpoint.Service.Namespace = storageNs
	}

	return k8s.CreateOrUpdateConsolePlugin(p.client, cp, ownerRef)
}

func updateDataIfNginxConfigMap(cm *v1.ConfigMap, storageNs string) {
	if cm.Name == NginxConfigMapName {
		cm.Data = map[string]string{
			"nginx.conf": `pid /tmp/nginx.pid;
    events {
      worker_connections 1024;
    }
    http {
      server {
        listen 8080;
          server_name px-plugin-proxy.` + storageNs + `.svc.cluster.local;
        location / {
          proxy_pass http://portworx-api.` + storageNs + `.svc.cluster.local:9021;
        }
      }
      server {
        listen 8443 ssl;
        server_name px-plugin-proxy.` + storageNs + `.svc.cluster.local;
        ssl_certificate /etc/nginx/certs/tls.crt;
        ssl_certificate_key /etc/nginx/certs/tls.key;
        location / {
          proxy_pass http://portworx-api.` + storageNs + `.svc.cluster.local:9021;
        }
      }
    }`,
		}
	}
}

func isVersionSupported(current, target string) bool {
	targetVersion, err := version.NewVersion(target)
	if err != nil {
		logrus.Errorf("Error during parsing version : %s ", err)
		return false
	}

	currentVersion, err := version.NewVersion(current)
	if err != nil {
		logrus.Errorf("Error during parsing version : %s ", err)
		return false
	}

	return currentVersion.GreaterThanOrEqual(targetVersion)
}

func getDesiredPluginImage(cluster *corev1.StorageCluster) string {
	imageName := manifest.DefaultDynamicPluginImage
	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.DynamicPlugin != "" {
		imageName = cluster.Status.DesiredImages.DynamicPlugin
	}
	imageName = util.GetImageURN(cluster, imageName)
	return imageName
}

func getDesiredPluginProxyImage(cluster *corev1.StorageCluster) string {
	imageName := manifest.DefaultDynamicPluginProxyImage

	if cluster.Status.DesiredImages != nil && cluster.Status.DesiredImages.DynamicPluginProxy != "" {
		imageName = cluster.Status.DesiredImages.DynamicPluginProxy
	}
	imageName = util.GetImageURN(cluster, imageName)
	return imageName
}
