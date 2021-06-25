package component

import (
	"fmt"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// TelemetryComponentName name of the telemetry component
	TelemetryComponentName = "Portworx Telemetry"
	// TelemetryConfigMapName is the name of the config map that stores the telemetry configuration for CCM
	TelemetryConfigMapName = "px-telemetry-config"
	// TelemetryPropertiesFilename is the name of the CCM properties file
	TelemetryPropertiesFilename = "ccm.properties"
	// TelemetryArcusLocationFilename is name of the file storing the location of arcus endpoint to use
	TelemetryArcusLocationFilename = "location"
)

type telemetry struct {
	k8sClient client.Client
}

func (t *telemetry) Name() string {
	return TelemetryComponentName
}

func (t *telemetry) Priority() int32 {
	return DefaultComponentPriority
}

func (t *telemetry) Initialize(k8sClient client.Client, _ version.Version, _ *runtime.Scheme, _ record.EventRecorder) {
	t.k8sClient = k8sClient
}

func (t *telemetry) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsTelemetryEnabled(cluster.Spec)
}

func (t *telemetry) Reconcile(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := t.createConfigMap(cluster, ownerRef); err != nil {
		return err
	}

	return nil
}

func (t *telemetry) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteConfigMap(t.k8sClient, TelemetryConfigMapName, cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (t *telemetry) MarkDeleted() {}

// RegisterTelemetryComponent registers the telemetry  component
func RegisterTelemetryComponent() {
	Register(TelemetryComponentName, &telemetry{})
}

func (t *telemetry) createConfigMap(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	config := fmt.Sprintf(`{
      "product_name": "portworx",
       "logging": {
         "array_info_path": "/dev/null"
       },
       "features": {
         "appliance_info": "config",
         "cert_store": "k8s",
         "config_reload": "file",
         "env_info": "file",
         "scheduled_log_uploader":"disabled",
         "upload": "enabled"
       },
      "cert": {
        "activation": {
              "private": "/dev/null",
              "public": "/dev/null"
        },
        "registration_enabled": "true",
        "no_rel_cert_enabled": "true",
        "appliance": {
          "current_cert_dir": "/etc/pwx/ccm/cert"
        }
      },
      "k8s": {
        "cert_secret_name": "pure-telemetry-certs",
        "cert_secret_namespace": "%s"
     },
     "cloud": {
       "array_loc_file_path": "/etc/ccm/%s"
     },
      "server": {
        "hostname": "0.0.0.0"
      },
      "logupload": {
        "logfile_patterns": [
            "/var/cores/*diags*",
            "/var/cores/auto/*diags*",
            "/var/cores/*px-cores*",
            "/var/cores/*.heap",
            "/var/cores/*.stack",
            "/var/cores/.alerts/alerts*"
        ],
        "skip_patterns": [],
        "additional_files": [
            "/etc/pwx/config.json",
            "/var/cores/.alerts/alerts.log",
            "/var/cores/px_etcd_watch.log",
            "/var/cores/px_cache_mon.log",
            "/var/cores/px_cache_mon_watch.log",
            "/var/cores/px_healthmon_watch.log",
            "/var/cores/px_event_watch.log"
        ],
        "phonehome_sent": "/var/cache/phonehome.sent"
      },
      "xml_rpc": {},
      "standalone": {
        "version": "1.0.0",
        "controller_sn": "SA-0",
        "component_name": "SA-0",
        "product_name": "portworx",
        "appliance_id_path": "/etc/pwx/cluster_uuid"
      },
      "subscription": {
        "use_appliance_id": "true"
      },
      "proxy": {
        "path": "/dev/null"
      }
    }
`, cluster.Namespace, TelemetryArcusLocationFilename)

	data := map[string]string{
		TelemetryPropertiesFilename: config,
	}

	if location, present := cluster.Annotations[pxutil.AnnotationTelemetryArcusLocation]; present && len(location) > 0 {
		data[TelemetryArcusLocationFilename] = location
	} else {
		data[TelemetryArcusLocationFilename] = "external"
	}

	return k8sutil.CreateOrUpdateConfigMap(
		t.k8sClient,
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:            TelemetryConfigMapName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Data: data,
		},
		ownerRef,
	)
}

func init() {
	RegisterTelemetryComponent()
}
