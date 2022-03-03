package migration

import (
	"context"
	"strconv"
	"strings"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (h *Handler) createStorageCluster(
	ds *appsv1.DaemonSet,
) (*corev1.StorageCluster, error) {
	stc := h.constructStorageCluster(ds)

	if err := h.addStorkSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addAutopilotSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addPVCControllerSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addMonitoringSpec(stc); err != nil {
		return nil, err
	}

	logrus.Infof("Creating StorageCluster %v/%v for migration", stc.Namespace, stc.Name)
	err := h.client.Create(context.TODO(), stc)
	if err == nil {
		stc.Status.Phase = constants.PhaseAwaitingApproval
		err = h.client.Status().Update(context.TODO(), stc)
	}
	return stc, err
}

func (h *Handler) constructStorageCluster(ds *appsv1.DaemonSet) *corev1.StorageCluster {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
	}

	c := getPortworxContainer(ds)
	cluster.Spec.Image = c.Image
	cluster.Spec.ImagePullPolicy = c.ImagePullPolicy
	if len(ds.Spec.Template.Spec.ImagePullSecrets) > 0 {
		cluster.Spec.ImagePullSecret = stringPtr(ds.Spec.Template.Spec.ImagePullSecrets[0].Name)
	}

	envMap := map[string]*v1.EnvVar{}
	kvdbSecret := struct {
		ca       string
		cert     string
		key      string
		aclToken string
		userpwd  string
	}{}
	miscArgs := ""

	for i := 0; i < len(c.Args); i++ {
		arg := c.Args[i]
		if arg == "-c" {
			cluster.Name = c.Args[i+1]
			i++
		} else if arg == "-x" {
			i++
		} else if arg == "-a" || arg == "-A" || arg == "-f" {
			initStorageSpec(cluster)
			if arg == "-a" {
				cluster.Spec.Storage.UseAll = boolPtr(true)
			}
			if arg == "-A" {
				cluster.Spec.Storage.UseAllWithPartitions = boolPtr(true)
			}
			if arg == "-f" {
				cluster.Spec.Storage.ForceUseDisks = boolPtr(true)
			}
		} else if arg == "-s" && strings.HasPrefix(c.Args[i+i], "/") {
			initStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.Storage.Devices != nil {
				devices = *cluster.Spec.Storage.Devices
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.Storage.Devices = &devices
			i++
		} else if arg == "-j" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.JournalDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-metadata" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.SystemMdDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-kvdb_dev" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			cluster.Spec.Storage.KvdbDevice = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-cache" && strings.HasPrefix(c.Args[i+1], "/") {
			initStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.Storage.CacheDevices != nil {
				devices = *cluster.Spec.Storage.CacheDevices
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.Storage.CacheDevices = &devices
			i++
		} else if arg == "-s" && !strings.HasPrefix(c.Args[i+i], "/") {
			initCloudStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.CloudStorage.DeviceSpecs != nil {
				devices = *cluster.Spec.CloudStorage.DeviceSpecs
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.CloudStorage.DeviceSpecs = &devices
			i++
		} else if arg == "-j" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.JournalDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-metadata" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.SystemMdDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-kvdb_dev" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.KvdbDeviceSpec = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-max_drive_set_count" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodes = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-max_storage_nodes_per_zone" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodesPerZone = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-max_storage_nodes_per_zone_per_nodegroup" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.MaxStorageNodesPerZonePerNodeGroup = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-node_pool_label" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.NodePoolLabel = c.Args[i+1]
			i++
		} else if arg == "-cloud_provider" {
			initCloudStorageSpec(cluster)
			cluster.Spec.CloudStorage.Provider = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-b" {
			initKvdbSpec(cluster)
			cluster.Spec.Kvdb.Internal = true
		} else if arg == "-k" {
			initKvdbSpec(cluster)
			cluster.Spec.Kvdb.Endpoints = strings.Split(c.Args[i+1], ",")
			i++
		} else if arg == "-ca" {
			kvdbSecret.ca = c.Args[i+1]
			i++
		} else if arg == "-cert" {
			kvdbSecret.cert = c.Args[i+1]
			i++
		} else if arg == "-key" {
			kvdbSecret.key = c.Args[i+1]
			i++
		} else if arg == "-acltoken" {
			kvdbSecret.aclToken = c.Args[i+1]
			i++
		} else if arg == "-userpwd" {
			kvdbSecret.userpwd = c.Args[i+1]
			i++
		} else if arg == "-d" {
			initNetworkSpec(cluster)
			cluster.Spec.Network.DataInterface = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-m" {
			initNetworkSpec(cluster)
			cluster.Spec.Network.MgmtInterface = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-secret_type" {
			cluster.Spec.SecretsProvider = stringPtr(c.Args[i+1])
			i++
		} else if arg == "-r" {
			cluster.Spec.StartPort = uint32Ptr(c.Args[i+1])
			i++
		} else if arg == "-rt_opts" {
			initRuntimeOptions(cluster)
			rtOpts := strings.Split(c.Args[i+1], ",")
			for _, opt := range rtOpts {
				s := strings.Split(opt, "=")
				cluster.Spec.RuntimeOpts[s[0]] = s[1]
			}
			i++
		} else if arg == "-marketplace_name" {
			envMap["MARKETPLACE_NAME"] = &v1.EnvVar{
				Name:  "MARKETPLACE_NAME",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-csi_endpoint" {
			envMap["CSI_ENDPOINT"] = &v1.EnvVar{
				Name:  "CSI_ENDPOINT",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-csiversion" {
			envMap["PORTWORX_CSIVERSION"] = &v1.EnvVar{
				Name:  "PORTWORX_CSIVERSION",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_issuer" {
			envMap["PORTWORX_AUTH_OIDC_ISSUER"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_ISSUER",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_client_id" {
			envMap["PORTWORX_AUTH_OIDC_CLIENTID"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_CLIENTID",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-oidc_custom_claim_namespace" {
			envMap["PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_OIDC_CUSTOM_NAMESPACE",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_issuer" {
			envMap["PORTWORX_AUTH_JWT_ISSUER"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_ISSUER",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_shared_secret" {
			envMap["PORTWORX_AUTH_JWT_SHAREDSECRET"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_SHAREDSECRET",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_rsa_pubkey_file" {
			envMap["PORTWORX_AUTH_JWT_RSA_PUBKEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_RSA_PUBKEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-jwt_ecds_pubkey_file" {
			envMap["PORTWORX_AUTH_JWT_ECDS_PUBKEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_JWT_ECDS_PUBKEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-username_claim" {
			envMap["PORTWORX_AUTH_USERNAME_CLAIM"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_USERNAME_CLAIM",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "-auth_system_key" {
			envMap["PORTWORX_AUTH_SYSTEM_KEY"] = &v1.EnvVar{
				Name:  "PORTWORX_AUTH_SYSTEM_KEY",
				Value: c.Args[i+1],
			}
			i++
		} else if arg == "--log" {
			cluster.Annotations[pxutil.AnnotationLogFile] = c.Args[i+1]
			i++
		} else {
			miscArgs += arg + " "
		}
	}

	// Populate env variables from args and env vars of portworx container
	for _, env := range c.Env {
		if env.Name == "PX_TEMPLATE_VERSION" {
			continue
		}
		envMap[env.Name] = env.DeepCopy()
	}
	if len(envMap) > 0 {
		cluster.Spec.Env = []v1.EnvVar{}
	}
	for _, env := range envMap {
		cluster.Spec.Env = append(cluster.Spec.Env, *env)
	}

	if len(miscArgs) > 0 {
		cluster.Annotations[pxutil.AnnotationMiscArgs] = strings.TrimSpace(miscArgs)
	}

	// Fill the placement strategy based on node affinity and tolerations
	if ds.Spec.Template.Spec.Affinity != nil &&
		ds.Spec.Template.Spec.Affinity.NodeAffinity != nil {
		cluster.Spec.Placement = &corev1.PlacementSpec{
			NodeAffinity: ds.Spec.Template.Spec.Affinity.NodeAffinity.DeepCopy(),
		}
	}
	if len(ds.Spec.Template.Spec.Tolerations) > 0 {
		if cluster.Spec.Placement == nil {
			cluster.Spec.Placement = &corev1.PlacementSpec{
				Tolerations: []v1.Toleration{},
			}
		}
		for _, t := range ds.Spec.Template.Spec.Tolerations {
			cluster.Spec.Placement.Tolerations = append(cluster.Spec.Placement.Tolerations, *(t.DeepCopy()))
		}
	}

	// Populate the update strategy
	if ds.Spec.UpdateStrategy.Type == appsv1.RollingUpdateDaemonSetStrategyType {
		cluster.Spec.UpdateStrategy.Type = corev1.RollingUpdateStorageClusterStrategyType
		maxUnavailable := intstr.FromInt(1)
		if ds.Spec.UpdateStrategy.RollingUpdate != nil {
			maxUnavailable = *ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable
		}
		cluster.Spec.UpdateStrategy.RollingUpdate = &corev1.RollingUpdateStorageCluster{
			MaxUnavailable: &maxUnavailable,
		}
	} else if ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
		cluster.Spec.UpdateStrategy.Type = corev1.OnDeleteStorageClusterStrategyType
	}

	// Enable CSI
	csiEnabled := false
	telemetryEnabled := false
	for _, c := range ds.Spec.Template.Spec.Containers {
		switch c.Name {
		case pxutil.CSIRegistrarContainerName:
			csiEnabled = true
		case pxutil.TelemetryContainerName:
			telemetryEnabled = true
		}
	}
	// Install snapshot controller, as Daemonset spec generator
	// always included snapshot controller.
	cluster.Spec.CSI = &corev1.CSISpec{
		Enabled: csiEnabled,
	}
	if csiEnabled {
		cluster.Spec.CSI.InstallSnapshotController = boolPtr(true)
	}
	cluster.Spec.Monitoring = &corev1.MonitoringSpec{
		Telemetry: &corev1.TelemetrySpec{
			Enabled: telemetryEnabled,
		},
	}

	_, hasSystemKey := envMap["PORTWORX_AUTH_SYSTEM_KEY"]
	_, hasJWTIssuer := envMap["PORTWORX_AUTH_JWT_ISSUER"]
	if hasSystemKey && hasJWTIssuer {
		// We don't support OIDC mode of authentication in the security spec.
		// User who are using OIDC will continue to use the env variables and
		// won't need to enable security through spec.security in StorageCluster.
		cluster.Spec.Security = &corev1.SecuritySpec{
			Enabled: true,
			Auth: &corev1.AuthSpec{
				GuestAccess: guestAccessTypePtr(corev1.GuestRoleManaged),
			},
		}
	}

	// TODO: Handle kvdb secret
	// TODO: Handle volumes
	// TODO: Handle custom annotations
	// TODO: Handle custom image registry

	return cluster
}

func (h *Handler) addStorkSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(storkDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: dep != nil,
	}
	return nil
}

func (h *Handler) addAutopilotSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(component.AutopilotDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	cluster.Spec.Autopilot = &corev1.AutopilotSpec{
		Enabled: dep != nil,
	}
	return nil
}

func (h *Handler) addPVCControllerSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(component.PVCDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	} else if dep == nil {
		return nil
	}
	// Explicitly enable pvc controller in kube system namespace as operator won't
	// enable it by default if running in kube-system namespace
	if cluster.Namespace == "kube-system" {
		cluster.Annotations[pxutil.AnnotationPVCController] = "true"
	}
	return nil
}

func (h *Handler) addMonitoringSpec(cluster *corev1.StorageCluster) error {
	// Check if metrics need to be exported
	svcMonitorFound := true
	svcMonitor := &monitoringv1.ServiceMonitor{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      serviceMonitorName,
			Namespace: cluster.Namespace,
		},
		svcMonitor,
	)
	if meta.IsNoMatchError(err) {
		return nil
	} else if errors.IsNotFound(err) {
		svcMonitorFound = false
	} else if err != nil {
		return err
	}
	if cluster.Spec.Monitoring == nil {
		cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	}
	cluster.Spec.Monitoring.Prometheus = &corev1.PrometheusSpec{
		ExportMetrics: svcMonitorFound,
	}

	// Check for alert manager
	alertManagerFound := true
	alertManager := &monitoringv1.Alertmanager{}
	err = h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      component.AlertManagerInstanceName,
			Namespace: cluster.Namespace,
		},
		alertManager,
	)
	if errors.IsNotFound(err) {
		alertManagerFound = false
	} else if err != nil {
		return err
	}
	cluster.Spec.Monitoring.Prometheus.AlertManager = &corev1.AlertManagerSpec{
		Enabled: alertManagerFound,
	}

	if !cluster.Spec.Monitoring.Prometheus.ExportMetrics &&
		!cluster.Spec.Monitoring.Prometheus.AlertManager.Enabled {
		return nil
	}

	// Check for prometheus
	prometheus := &monitoringv1.Prometheus{}
	err = h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      prometheusInstanceName,
			Namespace: cluster.Namespace,
		},
		prometheus,
	)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	if prometheus.Spec.RuleSelector != nil &&
		prometheus.Spec.RuleSelector.MatchLabels["prometheus"] == "portworx" &&
		prometheus.Spec.ServiceMonitorSelector != nil &&
		prometheus.Spec.ServiceMonitorSelector.MatchLabels["name"] == serviceMonitorName {
		cluster.Spec.Monitoring.Prometheus.Enabled = true
	}

	return nil
}

func (h *Handler) getDeployment(name, namespace string) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		dep,
	)
	if errors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return dep, nil
}

func initStorageSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Storage == nil {
		cluster.Spec.Storage = &corev1.StorageSpec{}
	}
}

func initCloudStorageSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.CloudStorage == nil {
		cluster.Spec.CloudStorage = &corev1.CloudStorageSpec{}
	}
}

func initNetworkSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Network == nil {
		cluster.Spec.Network = &corev1.NetworkSpec{}
	}
}

func initKvdbSpec(cluster *corev1.StorageCluster) {
	if cluster.Spec.Kvdb == nil {
		cluster.Spec.Kvdb = &corev1.KvdbSpec{}
	}
}

func initRuntimeOptions(cluster *corev1.StorageCluster) {
	if cluster.Spec.RuntimeOpts == nil {
		cluster.Spec.RuntimeOpts = map[string]string{}
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func stringSlicePtr(value []string) *[]string {
	return &value
}

func uint32Ptr(strValue string) *uint32 {
	v, _ := strconv.Atoi(strValue)
	value := uint32(v)
	return &value
}

func guestAccessTypePtr(value corev1.GuestAccessType) *corev1.GuestAccessType {
	return &value
}
