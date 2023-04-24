package migration

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"

	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	prometheusConfigReloaderArg             = "--prometheus-config-reloader="
	prometheusConfigMapReloaderArg          = "--config-reloader-image="
	csiProvisionerContainerName             = "csi-external-provisioner"
	csiAttacherContainerName                = "csi-attacher"
	csiSnapshotterContainerName             = "csi-snapshotter"
	csiResizerContainerName                 = "csi-resizer"
	csiSnapshotControllerContainerName      = "csi-snapshot-controller"
	csiHealthMonitorControllerContainerName = "csi-health-monitor-controller"
)

var (
	builtinPxVolumeNames = map[string]bool{
		"containerdsock":   true,
		"containerddir":    true,
		"containerdvardir": true,
		"cores":            true,
		"crioconf":         true,
		"criosock":         true,
		"dbusmount":        true,
		"dev":              true,
		"diagsdump":        true,
		"dockerplugins":    true,
		"dockersock":       true,
		"etcdcerts":        true,
		"etcpwx":           true,
		"hostproc":         true,
		"journalmount1":    true,
		"journalmount2":    true,
		"kubelet":          true,
		"libosd":           true,
		"optpwx":           true,
		"osddriver":        true,
		"procmount":        true,
		"pxlogs":           true,
		"src":              true,
		"sysdmount":        true,
	}
	skipPxEnvVarNames = map[string]bool{
		"PX_TEMPLATE_VERSION":          true,
		"PORTWORX_CSIVERSION":          true,
		"CSI_ENDPOINT":                 true,
		"NODE_NAME":                    true,
		"PX_KVDB_USERNAME":             true,
		"PX_KVDB_PASSWORD":             true,
		pxutil.EnvKeyPortworxNamespace: true,
	}
)

func (h *Handler) createStorageCluster(
	ds *appsv1.DaemonSet,
) (*corev1.StorageCluster, error) {
	stc, err := h.constructStorageCluster(ds)
	if err != nil {
		return nil, err
	}

	if err := h.addCSISpec(stc, ds); err != nil {
		return nil, err
	}
	if err := h.addStorkSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addAutopilotSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addPVCControllerSpec(stc); err != nil {
		return nil, err
	}
	if err := h.addMonitoringSpec(stc, ds); err != nil {
		return nil, err
	}

	if err := h.handleCustomImageRegistry(stc); err != nil {
		return nil, err
	}

	if err := h.createManifestConfigMap(stc); err != nil {
		return nil, err
	}

	logrus.Infof("Creating StorageCluster %v/%v for migration", stc.Namespace, stc.Name)
	err = h.client.Create(context.TODO(), stc)
	if err == nil {
		stc.Status.Phase = constants.PhaseAwaitingApproval
		err = h.client.Status().Update(context.TODO(), stc)
	}
	return stc, err
}

func parseCustomImageRegistry(pxImage string, componentImages []string) (string, bool) {
	// Let's remove docker.io prefix, so we don't take docker.io as custom image registry.
	if strings.HasPrefix(pxImage, util.DefaultImageRegistry) {
		pxImage = strings.TrimPrefix(pxImage, util.DefaultImageRegistry+"/")
	}

	// PX image could be
	// 1. portworx/oci-monitor:image
	// 2. custom-registry/oci-monitor:tag
	// 3. custom-registry/custom-repo/oci-monitor:tag
	imageParts := strings.Split(pxImage, "/")

	// This should not happen.
	if len(imageParts) <= 1 {
		return "", false
	}

	// Case 1: default format, such as portworx/oci-monitor:2.9.0
	if len(imageParts) == 2 && imageParts[0] == "portworx" {
		return "", false
	}

	// Case 2 & 3: remove image name and tag
	paths := imageParts[:len(imageParts)-1]
	for _, componentImage := range componentImages {
		if componentImage == "" {
			continue
		}
		registry := path.Join(paths...)
		// In case of some component images don't have custom registry,
		// we have to rely on "portworx" repo name of px image to determine whether it's case 3
		if !strings.HasPrefix(componentImage, registry) && (paths[len(paths)-1] == "portworx") {
			paths = paths[:len(paths)-1]
			return path.Join(paths...), true
		}
	}

	return path.Join(paths...), false
}

func (h *Handler) constructStorageCluster(ds *appsv1.DaemonSet) (*corev1.StorageCluster, error) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ds.Namespace,
			Annotations: map[string]string{
				constants.AnnotationMigrationApproved: "false",
			},
		},
		Status: corev1.StorageClusterStatus{
			DesiredImages: &corev1.ComponentImages{},
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
	autoJournalEnabled := false

	for i := 0; i < len(c.Args); i++ {
		arg := c.Args[i]
		if arg == "-c" {
			clusterID := c.Args[i+1]
			cluster.Name = getStorageClusterNameFromClusterID(clusterID)
			// If the cluster name got formatted, save the original cluster ID to storage cluster annotation
			if cluster.Name != clusterID {
				cluster.Annotations[pxutil.AnnotationClusterID] = clusterID
			}
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
		} else if arg == "-s" && strings.HasPrefix(c.Args[i+1], "/") {
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
		} else if arg == "-s" && !strings.HasPrefix(c.Args[i+1], "/") {
			initCloudStorageSpec(cluster)
			devices := []string{}
			if cluster.Spec.CloudStorage.DeviceSpecs != nil {
				devices = *cluster.Spec.CloudStorage.DeviceSpecs
			}
			devices = append(devices, c.Args[i+1])
			cluster.Spec.CloudStorage.DeviceSpecs = &devices
			i++
		} else if arg == "-j" && !strings.HasPrefix(c.Args[i+1], "/") {
			if c.Args[i+1] == "auto" {
				autoJournalEnabled = true
			} else {
				initCloudStorageSpec(cluster)
				cluster.Spec.CloudStorage.JournalDeviceSpec = stringPtr(c.Args[i+1])
			}
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

	if autoJournalEnabled {
		if cluster.Spec.Storage != nil || cluster.Spec.CloudStorage == nil {
			initStorageSpec(cluster)
			cluster.Spec.Storage.JournalDevice = stringPtr("auto")
		} else if cluster.Spec.CloudStorage != nil {
			cluster.Spec.CloudStorage.JournalDeviceSpec = stringPtr("auto")
		}
	}

	secretsNamespaceProvided := false
	// Populate env variables from args and env vars of portworx container
	for _, env := range c.Env {
		if ok := skipPxEnvVarNames[env.Name]; ok {
			continue
		}
		if env.Name == pxutil.EnvKeyPortworxSecretsNamespace {
			secretsNamespaceProvided = true
			if env.Value == cluster.Namespace {
				// No need to add this to StorageCluster as it will be added automatically
				// to the pod by the operator.
				continue
			}
		}
		envMap[env.Name] = env.DeepCopy()
	}
	if len(envMap) > 0 {
		cluster.Spec.Env = []v1.EnvVar{}
	}
	for _, env := range envMap {
		cluster.Spec.Env = append(cluster.Spec.Env, *env)
	}

	// Use default secrets namespace if not provided as Portworx assumes the namespace
	// to be 'portworx' in daemonset, while operator passes the StorageCluster's
	// namespace as the default secrets namespace.
	if !secretsNamespaceProvided {
		cluster.Spec.Env = append(cluster.Spec.Env, v1.EnvVar{
			Name:  pxutil.EnvKeyPortworxSecretsNamespace,
			Value: defaultSecretsNamespace,
		})
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

	// Populate StorageCluster spec.volumes from extra volumes mounted to portworx container
	if err := h.addExtraPortworxVolumes(cluster, c.VolumeMounts, ds.Spec.Template.Spec.Volumes); err != nil {
		return nil, err
	}

	// Add service type annotation based on portworx and portworx-api service if it's not ClusterIP by default
	if err := h.addServiceTypeAnnotation(cluster); err != nil {
		return nil, err
	}

	// Add StorageCluster custom annotations for storage pods if exist
	if err := h.addCustomAnnotations(cluster, ds); err != nil {
		return nil, err
	}

	// Handle external kvdb secret migration if configured
	if err := h.configureExternalKvdbAuth(cluster, ds, kvdbSecret.ca, kvdbSecret.cert, kvdbSecret.key, kvdbSecret.aclToken, kvdbSecret.userpwd); err != nil {
		return nil, err
	}

	return cluster, nil
}

func (h *Handler) addExtraPortworxVolumes(
	cluster *corev1.StorageCluster,
	mounts []v1.VolumeMount,
	volumes []v1.Volume,
) error {
	podTemplate, err := h.driver.GetStoragePodSpec(cluster, "")
	if err != nil {
		return err
	}

	var pxContainer *v1.Container
	for _, c := range podTemplate.Containers {
		if c.Name == portworxContainerName {
			pxContainer = c.DeepCopy()
			break
		}
	}
	if pxContainer == nil {
		return fmt.Errorf("failed to get portworx container template to migrate extra volumes")
	}

	mountPathMap := make(map[string]bool)
	for _, m := range pxContainer.VolumeMounts {
		if m.MountPath != "" {
			mountPathMap[m.MountPath] = true
		}
	}

	cluster.Spec.Volumes = getVolumeSpecs(mounts, volumes, builtinPxVolumeNames, mountPathMap)
	return nil
}

// configureExternalKvdbAuth migrate external kvdb auth configurations
// Find secret name from cert volume mounts or password env vars
// Certification auth:
// 1. Reuse the secret if cert file names are operator default ones
// 2. Create a new secret if cert file names are customized
// ACL token:
// 1. Reuse the secret if it has key acl-token
// 2. Create a new secret and store the arg value to it
// Password auth:
// 1. Reuse the secret if it has both key username and password
// 2. Create a new secret and store the arg values to it
func (h *Handler) configureExternalKvdbAuth(cluster *corev1.StorageCluster, ds *appsv1.DaemonSet, caPath, certPath, keyPath, aclToken, userpwd string) error {
	useCertificate := caPath != "" || certPath != "" || keyPath != ""
	useACLToken := aclToken != ""
	usePassword := userpwd != ""
	if !useCertificate && !useACLToken && !usePassword {
		return nil
	}

	// Need to create a new secret for cert auth if the keys don't match
	createNewSecret := false
	if (caPath != "" && path.Base(caPath) != portworx.SecretKeyKvdbCA) ||
		(certPath != "" && path.Base(certPath) != portworx.SecretKeyKvdbCert) ||
		(keyPath != "" && path.Base(keyPath) != portworx.SecretKeyKvdbCertKey) {
		createNewSecret = true
	}

	// Need to create a new secret for acl-token and password auth if secret or key not found
	secret := &v1.Secret{}
	secretName := getExternalKvdbSecretName(ds)
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      secretName,
			Namespace: ds.Namespace,
		},
		secret,
	)
	if errors.IsNotFound(err) {
		if useCertificate {
			return fmt.Errorf("kvdb certificate auth enabled but secret %s/%s not found", ds.Namespace, secretName)
		}
		if useACLToken {
			createNewSecret = true
		}
		if usePassword {
			createNewSecret = true
		}
	} else if err != nil {
		return err
	}

	// Try to get all values from the secret
	caVal, caExist := secret.Data[path.Base(caPath)]
	certVal, certExist := secret.Data[path.Base(certPath)]
	keyVal, keyExist := secret.Data[path.Base(keyPath)]
	aclTokenVal, aclTokenExist := secret.Data[portworx.SecretKeyKvdbACLToken]
	usernameVal, usernameExist := secret.Data[portworx.SecretKeyKvdbUsername]
	passwordVal, passwordExist := secret.Data[portworx.SecretKeyKvdbPassword]
	if (caPath != "" && !caExist) ||
		(certPath != "" && !certExist) ||
		(keyPath != "" && !keyExist) {
		return fmt.Errorf("kvdb certificate auth enabled but key(s) not found in secret %s/%s", ds.Namespace, secretName)
	}
	if useACLToken && !aclTokenExist ||
		usePassword && (!usernameExist || !passwordExist) {
		createNewSecret = true
	}

	// Case the existing secret will be reused
	if !createNewSecret {
		cluster.Spec.Kvdb.AuthSecret = secret.Name
		return nil
	}

	// Case new secret will be created
	newSecretData := make(map[string][]byte)
	if caPath != "" {
		newSecretData[portworx.SecretKeyKvdbCA] = caVal
	}
	if certPath != "" {
		newSecretData[portworx.SecretKeyKvdbCert] = certVal
	}
	if keyPath != "" {
		newSecretData[portworx.SecretKeyKvdbCertKey] = keyVal
	}
	if useACLToken {
		if aclTokenExist {
			newSecretData[portworx.SecretKeyKvdbACLToken] = aclTokenVal
		} else {
			newSecretData[portworx.SecretKeyKvdbACLToken] = []byte(aclToken)
		}
	}
	if usePassword {
		if usernameExist && passwordExist {
			newSecretData[portworx.SecretKeyKvdbUsername] = usernameVal
			newSecretData[portworx.SecretKeyKvdbPassword] = passwordVal
		} else {
			split := strings.Split(userpwd, ":")
			newSecretData[portworx.SecretKeyKvdbUsername] = []byte(split[0])
			newSecretData[portworx.SecretKeyKvdbPassword] = []byte(split[1])
		}
	}

	newSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-kvdb-auth-operator",
			Namespace: cluster.Namespace,
		},
		Data: newSecretData,
	}
	if err := h.client.Delete(context.TODO(), newSecret); err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err := h.client.Create(context.TODO(), newSecret); err != nil {
		return err
	}
	cluster.Spec.Kvdb.AuthSecret = newSecret.Name
	return nil
}

func getExternalKvdbSecretName(ds *appsv1.DaemonSet) string {
	// Assume certificate, acl token and password share the same secret
	// Look for etcdcerts volume for cert auth first
	for _, volume := range ds.Spec.Template.Spec.Volumes {
		if volume.Name == "etcdcerts" && volume.Secret != nil {
			return volume.Secret.SecretName
		}
	}
	// Look for username and password env vars if not passed using plain text
	container := getPortworxContainer(ds)
	for _, env := range container.Env {
		if env.Name == "PX_KVDB_USERNAME" || env.Name == "PX_KVDB_PASSWORD" {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				return env.ValueFrom.SecretKeyRef.Name
			}
		}
	}
	// For acl-token as it's not configured in spec generator, assuming it's passed by plain text
	return "px-kvdb-auth"
}

func getVolumeSpecs(
	mounts []v1.VolumeMount,
	volumes []v1.Volume,
	knownVolumeNames map[string]bool,
	existingVolumeMounts map[string]bool,
) []corev1.VolumeSpec {
	if knownVolumeNames == nil {
		knownVolumeNames = make(map[string]bool)
	}
	if existingVolumeMounts == nil {
		existingVolumeMounts = make(map[string]bool)
	}
	volumeSpecMap := make(map[string]*corev1.VolumeSpec)
	for _, m := range mounts {
		// Filter out builtin PX DaemonSet and Operator managed volumes by name
		// also ignore volumes with mount path conflicts
		if _, ok := knownVolumeNames[m.Name]; ok {
			continue
		} else if _, ok := existingVolumeMounts[m.MountPath]; ok {
			logrus.WithFields(logrus.Fields{
				"name":      m.Name,
				"mountPath": m.MountPath,
			}).Warningf("found mountPath conflict when constructing StorageCluster from DaemonSet, volume will be ignored")
			continue
		}
		volumeSpecMap[m.Name] = &corev1.VolumeSpec{
			Name:             m.Name,
			ReadOnly:         m.ReadOnly,
			MountPath:        m.MountPath,
			MountPropagation: m.MountPropagation,
		}
	}
	for _, v := range volumes {
		if spec, ok := volumeSpecMap[v.Name]; ok {
			spec.VolumeSource = *(v.VolumeSource.DeepCopy())
		}
	}

	var volumeSpecs []corev1.VolumeSpec
	for _, spec := range volumeSpecMap {
		volumeSpecs = append(volumeSpecs, *spec)
	}
	return volumeSpecs
}

func getEnvs(envs []v1.EnvVar, knownEnvNames map[string]bool) []v1.EnvVar {
	var filteredEnvs []v1.EnvVar
	for _, e := range envs {
		if _, ok := knownEnvNames[e.Name]; !ok {
			filteredEnvs = append(filteredEnvs, e)
		}
	}
	return filteredEnvs
}

func (h *Handler) removeCustomImageRegistry(customImageRegistry, image string) string {
	if image == "" || customImageRegistry == "" {
		return image
	}

	if !strings.HasPrefix(image, customImageRegistry) {
		logrus.Warningf("image %s does not have custom image registry prefix %s", image, customImageRegistry)
		return image
	}

	return strings.TrimPrefix(image, customImageRegistry+"/")
}

func (h *Handler) addCSISpec(cluster *corev1.StorageCluster, ds *appsv1.DaemonSet) error {
	// Enable CSI
	csiEnabled := false
	for _, c := range ds.Spec.Template.Spec.Containers {
		switch c.Name {
		case pxutil.CSIRegistrarContainerName:
			csiEnabled = true
			cluster.Status.DesiredImages.CSINodeDriverRegistrar = c.Image
		}
	}
	// Install snapshot controller, as Daemonset spec generator
	// always included snapshot controller.
	cluster.Spec.CSI = &corev1.CSISpec{
		Enabled: csiEnabled,
	}
	if csiEnabled {
		cluster.Spec.CSI.InstallSnapshotController = boolPtr(true)

		dep, err := h.getDeployment(component.CSIApplicationName, cluster.Namespace)
		if err != nil {
			return err
		} else if dep == nil {
			return nil
		}

		for _, c := range dep.Spec.Template.Spec.Containers {
			switch c.Name {
			case csiProvisionerContainerName:
				cluster.Status.DesiredImages.CSIProvisioner = c.Image
			case csiAttacherContainerName:
				cluster.Status.DesiredImages.CSIAttacher = c.Image
			case csiSnapshotterContainerName:
				cluster.Status.DesiredImages.CSISnapshotter = c.Image
			case csiResizerContainerName:
				cluster.Status.DesiredImages.CSIResizer = c.Image
			case csiSnapshotControllerContainerName:
				cluster.Status.DesiredImages.CSISnapshotController = c.Image
			case csiHealthMonitorControllerContainerName:
				cluster.Status.DesiredImages.CSIHealthMonitorController = c.Image
			}
		}
	}

	return nil
}

func (h *Handler) addStorkSpec(cluster *corev1.StorageCluster) error {
	dep, err := h.getDeployment(storkDeploymentName, cluster.Namespace)
	if err != nil {
		return err
	}
	cluster.Spec.Stork = &corev1.StorkSpec{
		Enabled: dep != nil,
	}

	if dep != nil {
		cluster.Status.DesiredImages.Stork = dep.Spec.Template.Spec.Containers[0].Image
		container := dep.Spec.Template.Spec.Containers[0]
		// Migrate extra env vars
		cluster.Spec.Stork.Env = getEnvs(container.Env, map[string]bool{"PX_SERVICE_NAME": true})
		// Migrate extra volumes
		cluster.Spec.Stork.Volumes = getVolumeSpecs(container.VolumeMounts, dep.Spec.Template.Spec.Volumes, nil, nil)
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

	if dep != nil {
		cluster.Status.DesiredImages.Autopilot = dep.Spec.Template.Spec.Containers[0].Image
		container := dep.Spec.Template.Spec.Containers[0]
		// Migrate extra env vars
		cluster.Spec.Autopilot.Env = getEnvs(container.Env, make(map[string]bool))
		// Migrate extra volumes
		cluster.Spec.Autopilot.Volumes = getVolumeSpecs(container.VolumeMounts, dep.Spec.Template.Spec.Volumes, map[string]bool{"config-volume": true}, nil)
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

func (h *Handler) getContainerImage(statefulSet *appsv1.StatefulSet, containerName string) string {
	for _, c := range statefulSet.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}

	return ""
}

// addServiceTypeAnnotation scans services and set the storage cluster annotation
// if the service type is not the default one
// e.g. portworx.io/service-type: "portworx-service:LoadBalancer;portworx-kvdb-service:LoadBalancer"
func (h *Handler) addServiceTypeAnnotation(cluster *corev1.StorageCluster) error {
	annotation := ""

	// Service portworx-service has ClusterIP by default
	portworxService, err := h.getService(pxutil.PortworxServiceName, cluster.Namespace)
	if err != nil {
		return err
	}
	if portworxService != nil && portworxService.Spec.Type != v1.ServiceTypeClusterIP {
		annotation = fmt.Sprintf("%s:%s", portworxService.Name, portworxService.Spec.Type)
	}

	// Service portworx-api has ClusterIP by default
	portworxAPIService, err := h.getService(component.PxAPIServiceName, cluster.Namespace)
	if err != nil {
		return err
	}
	if portworxAPIService != nil && portworxAPIService.Spec.Type != v1.ServiceTypeClusterIP {
		part := fmt.Sprintf("%s:%s", portworxAPIService.Name, portworxAPIService.Spec.Type)
		if annotation == "" {
			annotation = part
		} else {
			annotation = fmt.Sprintf("%s;%s", annotation, part)
		}
	}

	if annotation != "" {
		cluster.Annotations[pxutil.AnnotationServiceType] = annotation
	}

	return nil
}

func (h *Handler) addMonitoringSpec(cluster *corev1.StorageCluster, ds *appsv1.DaemonSet) error {
	if cluster.Spec.Monitoring == nil {
		cluster.Spec.Monitoring = &corev1.MonitoringSpec{}
	}

	// Check for telemetry
	telemetryImage := ""
	for _, container := range ds.Spec.Template.Spec.Containers {
		if strings.Contains(container.Image, "ccm-service") {
			telemetryImage = container.Image
			break
		}
	}
	cluster.Spec.Monitoring.Telemetry = &corev1.TelemetrySpec{
		Enabled: telemetryImage != "",
	}
	cluster.Status.DesiredImages.Telemetry = telemetryImage

	// Telemetry components are removed from PX DaemonSet 2.12+ so it's not a valid cluster setup to migrate.
	// CCM upgrade to a newer version during migration is not supported, so we disable telemetry if end up here.
	if pxutil.GetPortworxVersion(cluster).GreaterThanOrEqual(pxutil.MinimumPxVersionCCMGO) {
		cluster.Spec.Monitoring.Telemetry.Enabled = false
		cluster.Status.DesiredImages.Telemetry = ""
		msg := "PX 2.12+ DaemonSet migration with telemetry enabled is not supported, telemetry will be disabled during migration. " +
			"Re-enable it from StorageCluster spec once migration succeeded to use telemetry again."
		k8sutil.WarningEvent(h.ctrl.GetEventRecorder(), cluster, util.FailedComponentReason, msg)
	}

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
	statefulSetList := &appsv1.StatefulSetList{}
	err = h.client.List(
		context.TODO(),
		statefulSetList,
		&client.ListOptions{
			Namespace: ds.Namespace,
		},
	)
	if err != nil {
		return err
	}

	if alertManagerFound {
		if alertManager.Spec.Image != nil {
			cluster.Status.DesiredImages.AlertManager = *alertManager.Spec.Image
		} else {
			for _, ss := range statefulSetList.Items {
				if metav1.IsControlledBy(&ss, alertManager) {
					cluster.Status.DesiredImages.AlertManager = h.getContainerImage(&ss, "alertmanager")
					break
				}
			}
		}
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

		if prometheus.Spec.Image != nil {
			cluster.Status.DesiredImages.Prometheus = *prometheus.Spec.Image
		} else {
			for _, ss := range statefulSetList.Items {
				if metav1.IsControlledBy(&ss, prometheus) {
					cluster.Status.DesiredImages.Prometheus = h.getContainerImage(&ss, "prometheus")
					break
				}
			}
		}

		dep := &appsv1.Deployment{}
		err := h.client.Get(
			context.TODO(),
			types.NamespacedName{
				Name:      prometheusOpDeploymentName,
				Namespace: cluster.Namespace,
			},
			dep,
		)
		if errors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}

		container := dep.Spec.Template.Spec.Containers[0]
		cluster.Status.DesiredImages.PrometheusOperator = container.Image

		for _, arg := range container.Args {
			if strings.HasPrefix(arg, prometheusConfigReloaderArg) {
				cluster.Status.DesiredImages.PrometheusConfigReloader = strings.TrimPrefix(arg, prometheusConfigReloaderArg)
			}
			if strings.HasPrefix(arg, prometheusConfigMapReloaderArg) {
				cluster.Status.DesiredImages.PrometheusConfigMapReload = strings.TrimPrefix(arg, prometheusConfigMapReloaderArg)
			}
		}

		if cluster.Status.DesiredImages.PrometheusConfigReloader == "" {
			imgVersion := strings.Split(cluster.Status.DesiredImages.PrometheusOperator, ":")[1]
			cluster.Status.DesiredImages.PrometheusConfigReloader = "quay.io/coreos/prometheus-config-reloader:" + imgVersion
		}
	}

	return nil
}

func (h *Handler) addCustomAnnotations(cluster *corev1.StorageCluster, ds *appsv1.DaemonSet) error {
	customAnnotations := make(map[string]string)
	for k, v := range ds.Spec.Template.Annotations {
		if k != constants.AnnotationPodSafeToEvict {
			customAnnotations[k] = v
		}
	}

	if len(customAnnotations) != 0 {
		if cluster.Spec.Metadata == nil {
			cluster.Spec.Metadata = &corev1.Metadata{}
		}
		if cluster.Spec.Metadata.Annotations == nil {
			cluster.Spec.Metadata.Annotations = make(map[string]map[string]string)
		}
		cluster.Spec.Metadata.Annotations["pod/storage"] = customAnnotations
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

func (h *Handler) getService(name, namespace string) (*v1.Service, error) {
	svc := &v1.Service{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		svc,
	)
	if errors.IsNotFound(err) {
		logrus.Warningf("service %s/%s not found", namespace, name)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return svc, nil
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

func getStorageClusterNameFromClusterID(clusterID string) string {
	// Must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character
	// If the cluster ID is a valid k8s object name, just set it as the storage cluster name
	// Additionally, check whether the clusterID is longer than 63 chars, which will fail to construct controller revision
	validNameRegex := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	if validNameRegex.MatchString(clusterID) && len(clusterID) <= 63 {
		return clusterID
	}

	// Truncate the cluster ID if it's longer than 63 chars
	if len(clusterID) > 63 {
		clusterID = clusterID[0:63]
	}

	// If the cluster ID is invalid as a k8s object name, replace '_' with '-' then remove all invalid characters
	invalidCharRegex := regexp.MustCompile(`[^a-z0-9-]+`)
	clusterID = strings.Trim(strings.ReplaceAll(clusterID, "_", "-"), "-")
	return invalidCharRegex.ReplaceAllString(strings.ToLower(clusterID), "")
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

func (h *Handler) handleCustomImageRegistry(cluster *corev1.StorageCluster) error {
	var componentImages []string
	desiredImages := cluster.Status.DesiredImages
	componentImages = append(componentImages, desiredImages.Stork)
	componentImages = append(componentImages, desiredImages.UserInterface)
	componentImages = append(componentImages, desiredImages.Autopilot)
	componentImages = append(componentImages, desiredImages.CSINodeDriverRegistrar)
	componentImages = append(componentImages, desiredImages.CSIDriverRegistrar)
	componentImages = append(componentImages, desiredImages.CSIProvisioner)
	componentImages = append(componentImages, desiredImages.CSIAttacher)
	componentImages = append(componentImages, desiredImages.CSIResizer)
	componentImages = append(componentImages, desiredImages.CSISnapshotter)
	componentImages = append(componentImages, desiredImages.CSISnapshotController)
	componentImages = append(componentImages, desiredImages.CSIHealthMonitorController)
	componentImages = append(componentImages, desiredImages.PrometheusOperator)
	componentImages = append(componentImages, desiredImages.PrometheusConfigMapReload)
	componentImages = append(componentImages, desiredImages.PrometheusConfigReloader)
	componentImages = append(componentImages, desiredImages.Prometheus)
	componentImages = append(componentImages, desiredImages.AlertManager)
	componentImages = append(componentImages, desiredImages.Telemetry)
	componentImages = append(componentImages, desiredImages.MetricsCollector)
	componentImages = append(componentImages, desiredImages.MetricsCollectorProxy)

	cluster.Spec.CustomImageRegistry, cluster.Spec.PreserveFullCustomImageRegistry = parseCustomImageRegistry(cluster.Spec.Image, componentImages)

	cluster.Spec.Image = h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Spec.Image)
	cluster.Status.DesiredImages = &corev1.ComponentImages{
		Stork:                      h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.Stork),
		UserInterface:              h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.UserInterface),
		Autopilot:                  h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.Autopilot),
		CSINodeDriverRegistrar:     h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSINodeDriverRegistrar),
		CSIDriverRegistrar:         h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSIDriverRegistrar),
		CSIProvisioner:             h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSIProvisioner),
		CSIAttacher:                h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSIAttacher),
		CSIResizer:                 h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSIResizer),
		CSISnapshotter:             h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSISnapshotter),
		CSISnapshotController:      h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSISnapshotController),
		CSIHealthMonitorController: h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.CSIHealthMonitorController),
		PrometheusOperator:         h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.PrometheusOperator),
		PrometheusConfigMapReload:  h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.PrometheusConfigMapReload),
		PrometheusConfigReloader:   h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.PrometheusConfigReloader),
		Prometheus:                 h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.Prometheus),
		AlertManager:               h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.AlertManager),
		Telemetry:                  h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.Telemetry),
		MetricsCollector:           h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.MetricsCollector),
		MetricsCollectorProxy:      h.removeCustomImageRegistry(cluster.Spec.CustomImageRegistry, cluster.Status.DesiredImages.MetricsCollectorProxy),
	}
	cluster.Status.Version = pxutil.GetImageTag(cluster.Spec.Image)
	return nil
}

func (h *Handler) createManifestConfigMap(cluster *corev1.StorageCluster) error {
	// This is not air-gapped env, there is no need to create the configmap.
	if manifest.Instance().CanAccessRemoteManifest(cluster) {
		return nil
	}

	versionCM := &v1.ConfigMap{}
	err := h.client.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      manifest.DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		versionCM,
	)

	// configmap exists, in this case let's clear DesiredImages and use images in the configmap.
	if err == nil {
		cluster.Status.DesiredImages = &corev1.ComponentImages{}
		cluster.Status.Version = ""
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}

	splits := strings.Split(cluster.Spec.Image, ":")
	ver := manifest.Version{
		PortworxVersion: splits[len(splits)-1],
		Components: manifest.Release{
			Stork:                      cluster.Status.DesiredImages.Stork,
			Lighthouse:                 "",
			Autopilot:                  cluster.Status.DesiredImages.Autopilot,
			NodeWiper:                  "",
			CSIDriverRegistrar:         cluster.Status.DesiredImages.CSIDriverRegistrar,
			CSINodeDriverRegistrar:     cluster.Status.DesiredImages.CSINodeDriverRegistrar,
			CSIProvisioner:             cluster.Status.DesiredImages.CSIProvisioner,
			CSIAttacher:                cluster.Status.DesiredImages.CSIAttacher,
			CSIResizer:                 cluster.Status.DesiredImages.CSIResizer,
			CSISnapshotter:             cluster.Status.DesiredImages.CSISnapshotter,
			CSISnapshotController:      cluster.Status.DesiredImages.CSISnapshotController,
			CSIHealthMonitorController: cluster.Status.DesiredImages.CSIHealthMonitorController,
			Prometheus:                 cluster.Status.DesiredImages.Prometheus,
			AlertManager:               cluster.Status.DesiredImages.AlertManager,
			PrometheusOperator:         cluster.Status.DesiredImages.PrometheusOperator,
			PrometheusConfigMapReload:  cluster.Status.DesiredImages.PrometheusConfigMapReload,
			PrometheusConfigReloader:   cluster.Status.DesiredImages.PrometheusConfigReloader,
			Telemetry:                  cluster.Status.DesiredImages.Telemetry,
			MetricsCollector:           cluster.Status.DesiredImages.MetricsCollector,
			MetricsCollectorProxy:      cluster.Status.DesiredImages.MetricsCollectorProxy,
		},
	}

	bytes, err := yaml.Marshal(ver)
	if err != nil {
		return err
	}

	versionCM.Name = manifest.DefaultConfigMapName
	versionCM.Namespace = cluster.Namespace
	versionCM.Data = make(map[string]string)
	versionCM.Data[manifest.VersionConfigMapKey] = string(bytes)
	return h.client.Create(
		context.TODO(),
		versionCM,
	)
}
