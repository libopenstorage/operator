package portworx

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/google/shlex"
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/cloudops"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pxContainerName            = "portworx"
	pxAnnotationPrefix         = "portworx.io"
	annotationIsPKS            = pxAnnotationPrefix + "/is-pks"
	annotationIsGKE            = pxAnnotationPrefix + "/is-gke"
	annotationIsAKS            = pxAnnotationPrefix + "/is-aks"
	annotationIsEKS            = pxAnnotationPrefix + "/is-eks"
	annotationIsOpenshift      = pxAnnotationPrefix + "/is-openshift"
	annotationPVCController    = pxAnnotationPrefix + "/pvc-controller"
	annotationLogFile          = pxAnnotationPrefix + "/log-file"
	annotationMiscArgs         = pxAnnotationPrefix + "/misc-args"
	annotationPVCControllerCPU = pxAnnotationPrefix + "/pvc-controller-cpu"
	annotationAutopilotCPU     = pxAnnotationPrefix + "/autopilot-cpu"
	annotationServiceType      = pxAnnotationPrefix + "/service-type"
	annotationPXVersion        = pxAnnotationPrefix + "/px-version"
	templateVersion            = "v4"
	secretKeyKvdbCA            = "kvdb-ca.crt"
	secretKeyKvdbCert          = "kvdb.crt"
	secretKeyKvdbCertKey       = "kvdb.key"
	secretKeyKvdbUsername      = "username"
	secretKeyKvdbPassword      = "password"
	secretKeyKvdbACLToken      = "acl-token"
	envKeyPXImage              = "PX_IMAGE"
)

type volumeInfo struct {
	name             string
	hostPath         string
	mountPath        string
	readOnly         bool
	mountPropagation *v1.MountPropagationMode
	hostPathType     *v1.HostPathType
	pks              *pksVolumeInfo
}

type pksVolumeInfo struct {
	hostPath  string
	mountPath string
}

var (
	// defaultVolumeInfoList is a list of volumes across all options
	defaultVolumeInfoList = []volumeInfo{
		{
			name:      "diagsdump",
			hostPath:  "/var/cores",
			mountPath: "/var/cores",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/store/cores",
			},
		},
		{
			name:      "dockersock",
			hostPath:  "/var/run/docker.sock",
			mountPath: "/var/run/docker.sock",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/sys/run/docker/docker.sock",
			},
		},
		{
			name:      "containerdsock",
			hostPath:  "/run/containerd",
			mountPath: "/run/containerd",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/sys/run/containerd",
			},
		},
		{
			name:      "criosock",
			hostPath:  "/var/run/crio",
			mountPath: "/var/run/crio",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/sys/run/crio",
			},
		},
		{
			name:      "crioconf",
			hostPath:  "/etc/crictl.yaml",
			mountPath: "/etc/crictl.yaml",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/store/crictl.yaml",
			},
			hostPathType: hostPathTypePtr(v1.HostPathFileOrCreate),
		},
		{
			name:      "etcpwx",
			hostPath:  "/etc/pwx",
			mountPath: "/etc/pwx",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/store/etc/pwx",
			},
		},
		{
			name: "pxlogs",
			pks: &pksVolumeInfo{
				hostPath:  "/var/vcap/store/lib/osd/log",
				mountPath: "/var/lib/osd/log",
			},
		},
		{
			name:      "optpwx",
			hostPath:  "/opt/pwx",
			mountPath: "/opt/pwx",
			pks: &pksVolumeInfo{
				hostPath: "/var/vcap/store/opt/pwx",
			},
		},
		{
			name:      "procmount",
			hostPath:  "/proc",
			mountPath: "/host_proc",
		},
		{
			name:      "sysdmount",
			hostPath:  "/etc/systemd/system",
			mountPath: "/etc/systemd/system",
		},
		{
			name:      "journalmount1",
			hostPath:  "/var/run/log",
			mountPath: "/var/run/log",
			readOnly:  true,
		},
		{
			name:      "journalmount2",
			hostPath:  "/var/log",
			mountPath: "/var/log",
			readOnly:  true,
		},
		{
			name:      "dbusmount",
			hostPath:  "/var/run/dbus",
			mountPath: "/var/run/dbus",
		},
	}

	// kvdbVolumeInfo has information of the volume needed for kvdb certs
	kvdbVolumeInfo = volumeInfo{
		name:      "kvdbcerts",
		mountPath: "/etc/pwx/kvdbcerts",
	}
)

type template struct {
	cluster         *corev1alpha1.StorageCluster
	isPKS           bool
	isOpenshift     bool
	imagePullPolicy v1.PullPolicy
	serviceType     v1.ServiceType
	startPort       int
	k8sVersion      *version.Version
	pxVersion       *version.Version
	csiConfig       *pxutil.CSIConfiguration
	kvdb            map[string]string
	cloudConfig     *cloudstorage.Config
}

func newTemplate(
	cluster *corev1alpha1.StorageCluster,
) (*template, error) {
	if cluster == nil {
		return nil, fmt.Errorf("storage cluster cannot be empty")
	}

	t := &template{cluster: cluster}

	var err error
	t.k8sVersion, err = k8sutil.GetVersion()
	if err != nil {
		return nil, err
	}

	t.pxVersion = pxutil.GetPortworxVersion(cluster)
	deprecatedCSIDriverName := pxutil.UseDeprecatedCSIDriverName(cluster)
	csiGenerator := pxutil.NewCSIGenerator(*t.k8sVersion, *t.pxVersion, deprecatedCSIDriverName)
	if pxutil.FeatureCSI.IsEnabled(cluster.Spec.FeatureGates) {
		t.csiConfig = csiGenerator.GetCSIConfiguration()
	} else {
		t.csiConfig = csiGenerator.GetBasicCSIConfiguration()
	}

	t.isPKS = pxutil.IsPKS(cluster)
	t.isOpenshift = pxutil.IsOpenshift(cluster)
	t.serviceType = pxutil.ServiceType(cluster)
	t.imagePullPolicy = pxutil.ImagePullPolicy(cluster)
	t.startPort = pxutil.StartPort(cluster)

	return t, nil
}

func (p *portworx) generateCloudStorageSpecs(
	cluster *corev1alpha1.StorageCluster,
	nodes []*corev1alpha1.StorageNode,
) (*cloudstorage.Config, error) {

	var cloudConfig *cloudstorage.Config
	var err error

	cloudConfig = p.storageNodeToCloudSpec(nodes, cluster)

	if cloudConfig == nil {
		instancesPerZone := 0
		if cluster.Spec.CloudStorage.MaxStorageNodesPerZone != nil {
			instancesPerZone = int(*cluster.Spec.CloudStorage.MaxStorageNodesPerZone)
		}

		cloudStorageManager := &portworxCloudStorage{
			p.zoneToInstancesMap,
			cloudops.ProviderType(p.cloudProvider),
			cluster.Namespace,
			p.k8sClient,
			metav1.NewControllerRef(cluster, pxutil.StorageClusterKind()),
		}

		if err = cloudStorageManager.CreateStorageDistributionMatrix(); err != nil {
			logrus.Warnf("Failed to generate storage distribution matrix config map: %v", err)
		}

		cloudConfig, err = cloudStorageManager.GetStorageNodeConfig(
			cluster.Spec.CloudStorage.CapacitySpecs,
			instancesPerZone,
		)
		if err != nil {
			return nil, fmt.Errorf("unable to get cloud storage node config: %v", err)
		}
		return cloudConfig, err
	}
	return cloudConfig, nil
}

// TODO [Imp] Validate the cluster spec and return errors in the configuration
func (p *portworx) GetStoragePodSpec(
	cluster *corev1alpha1.StorageCluster, nodeName string,
) (v1.PodSpec, error) {

	t, err := newTemplate(cluster)
	if err != nil {
		return v1.PodSpec{}, err
	}

	if cluster.Spec.CloudStorage != nil && len(cluster.Spec.CloudStorage.CapacitySpecs) > 0 {
		nodes, err := p.storageNodesList(cluster)
		if err != nil {
			return v1.PodSpec{}, err
		}

		cloudConfig, err := p.generateCloudStorageSpecs(cluster, nodes)
		if err != nil {
			return v1.PodSpec{}, err
		}

		if !storageNodeExists(nodeName, nodes) {
			err = p.createStorageNode(cluster, nodeName, cloudConfig)
			if err != nil {
				msg := fmt.Sprintf("Failed to create node for nodeID %v: %v", nodeName, err)
				p.warningEvent(cluster, util.FailedSyncReason, msg)
			}
		}
		t.cloudConfig = cloudConfig
	}

	containers := t.portworxContainer()
	podSpec := v1.PodSpec{
		HostNetwork:        true,
		RestartPolicy:      v1.RestartPolicyAlways,
		ServiceAccountName: pxutil.PortworxServiceAccountName,
		Containers:         []v1.Container{containers},
		Volumes:            t.getVolumes(),
	}

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		csiRegistrar := t.csiRegistrarContainer()
		if csiRegistrar != nil {
			podSpec.Containers = append(podSpec.Containers, *csiRegistrar)
		}
	}

	if t.cluster.Spec.Placement != nil && t.cluster.Spec.Placement.NodeAffinity != nil {
		podSpec.Affinity = &v1.Affinity{
			NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
		}
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *t.cluster.Spec.ImagePullSecret,
			},
		)
	}

	return podSpec, nil
}

func (p *portworx) createStorageNode(cluster *corev1alpha1.StorageCluster, nodeName string, cloudConfig *cloudstorage.Config) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	storageNode := &corev1alpha1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            nodeName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          p.GetSelectorLabels(),
		},
		Status: corev1alpha1.NodeStatus{},
	}

	configureStorageNodeSpec(storageNode, cloudConfig)
	if cluster.Status.Storage.StorageNodesPerZone != cloudConfig.StorageInstancesPerZone {
		cluster.Status.Storage.StorageNodesPerZone = cloudConfig.StorageInstancesPerZone
		err := k8sutil.UpdateStorageClusterStatus(p.k8sClient, cluster)
		if err != nil {
			return err
		}
	}

	err := p.k8sClient.Create(context.TODO(), storageNode)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (p *portworx) storageNodesList(cluster *corev1alpha1.StorageCluster) ([]*corev1alpha1.StorageNode, error) {

	nodes := &corev1alpha1.StorageNodeList{}
	storageNodes := make([]*corev1alpha1.StorageNode, 0)

	err := p.k8sClient.List(context.TODO(), nodes,
		&client.ListOptions{Namespace: cluster.Namespace})

	if err != nil {
		return storageNodes, err
	}

	for _, node := range nodes.Items {
		controllerRef := metav1.GetControllerOf(&node)
		if controllerRef != nil && controllerRef.UID == cluster.UID {
			storageNodes = append(storageNodes, node.DeepCopy())
		}
	}

	return storageNodes, nil
}

func storageNodeExists(nodeName string, nodes []*corev1alpha1.StorageNode) bool {
	for _, node := range nodes {
		if nodeName == node.Name {
			return true
		}
	}
	return false
}

func configureStorageNodeSpec(node *corev1alpha1.StorageNode, config *cloudstorage.Config) {
	node.Spec = corev1alpha1.StorageNodeSpec{CloudStorage: corev1alpha1.StorageNodeCloudDriveConfigs{}}
	for _, conf := range config.CloudStorage {
		sc := corev1alpha1.StorageNodeCloudDriveConfig{
			Type:      conf.Type,
			IOPS:      conf.IOPS,
			SizeInGiB: conf.SizeInGiB,
			Options:   conf.Options,
		}
		node.Spec.CloudStorage.DriveConfigs = append(node.Spec.CloudStorage.DriveConfigs, sc)
	}
}

func (t *template) portworxContainer() v1.Container {
	pxImage := util.GetImageURN(t.cluster.Spec.CustomImageRegistry, t.cluster.Spec.Image)
	return v1.Container{
		Name:            pxContainerName,
		Image:           pxImage,
		ImagePullPolicy: t.imagePullPolicy,
		Args:            t.getArguments(),
		Env:             t.getEnvList(),
		LivenessProbe: &v1.Probe{
			PeriodSeconds:       30,
			InitialDelaySeconds: 840,
			Handler: v1.Handler{
				HTTPGet: &v1.HTTPGetAction{
					Host: "127.0.0.1",
					Path: "/status",
					Port: intstr.FromInt(t.startPort),
				},
			},
		},
		ReadinessProbe: &v1.Probe{
			PeriodSeconds: 10,
			Handler: v1.Handler{
				HTTPGet: &v1.HTTPGetAction{
					Host: "127.0.0.1",
					Path: "/health",
					Port: intstr.FromInt(t.startPort + 14),
				},
			},
		},
		TerminationMessagePath: "/tmp/px-termination-log",
		SecurityContext: &v1.SecurityContext{
			Privileged: boolPtr(true),
		},
		VolumeMounts: t.getVolumeMounts(),
	}
}

func (t *template) csiRegistrarContainer() *v1.Container {
	container := v1.Container{
		ImagePullPolicy: t.imagePullPolicy,
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: "/csi/csi.sock",
			},
			{
				Name: "KUBE_NODE_NAME",
				ValueFrom: &v1.EnvVarSource{
					FieldRef: &v1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
		},
		SecurityContext: &v1.SecurityContext{
			Privileged: boolPtr(true),
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      "csi-driver-path",
				MountPath: "/csi",
			},
			{
				Name:      "registration-dir",
				MountPath: "/registration",
			},
		},
	}

	if t.csiConfig.NodeRegistrar != "" {
		container.Name = "csi-node-driver-registrar"
		container.Image = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.csiConfig.NodeRegistrar,
		)
		container.Args = []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			fmt.Sprintf("--kubelet-registration-path=%s/csi.sock", t.csiConfig.DriverBasePath()),
		}
	} else if t.csiConfig.Registrar != "" {
		container.Name = "csi-driver-registrar"
		container.Image = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.csiConfig.Registrar,
		)
		container.Args = []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			"--mode=node-register",
			fmt.Sprintf("--kubelet-registration-path=%s/csi.sock", t.csiConfig.DriverBasePath()),
		}
	}

	if container.Name == "" {
		return nil
	}
	return &container
}

func (t *template) getSelectorRequirements() []v1.NodeSelectorRequirement {
	selectorRequirements := []v1.NodeSelectorRequirement{
		{
			Key:      "px/enabled",
			Operator: v1.NodeSelectorOpNotIn,
			Values:   []string{"false"},
		},
	}

	if t.isOpenshift {
		selectorRequirements = append(
			selectorRequirements,
			v1.NodeSelectorRequirement{
				Key:      "node-role.kubernetes.io/infra",
				Operator: v1.NodeSelectorOpDoesNotExist,
			},
		)
	}
	selectorRequirements = append(
		selectorRequirements,
		v1.NodeSelectorRequirement{
			Key:      "node-role.kubernetes.io/master",
			Operator: v1.NodeSelectorOpDoesNotExist,
		},
	)

	return selectorRequirements
}

func (t *template) getCloudStorageArguments(cloudDeviceSpec cloudstorage.CloudDriveConfig) string {
	devSpec := strings.Join(
		[]string{
			"type=" + cloudDeviceSpec.Type,
			"size=" + strconv.FormatUint(cloudDeviceSpec.SizeInGiB, 10),
			"iops=" + strconv.FormatUint(uint64(cloudDeviceSpec.IOPS), 10)},
		",",
	)
	for k, v := range cloudDeviceSpec.Options {
		devSpec = strings.Join(
			[]string{
				devSpec,
				k + "=" + v,
			},
			",",
		)
	}
	return devSpec

}

func (t *template) getArguments() []string {
	args := []string{
		"-c", t.cluster.Name,
		"-x", "kubernetes",
	}

	if t.cluster.Spec.Kvdb != nil {
		if t.cluster.Spec.Kvdb.Internal {
			args = append(args, "-b")
		}
		if len(t.cluster.Spec.Kvdb.Endpoints) != 0 {
			args = append(args, "-k", strings.Join(t.cluster.Spec.Kvdb.Endpoints, ","))
		}

		auth := t.loadKvdbAuth()
		if auth[secretKeyKvdbCert] != "" {
			args = append(args, "-cert", path.Join(kvdbVolumeInfo.mountPath, secretKeyKvdbCert))
			if auth[secretKeyKvdbCA] != "" {
				args = append(args, "-ca", path.Join(kvdbVolumeInfo.mountPath, secretKeyKvdbCA))
			}
			if auth[secretKeyKvdbCertKey] != "" {
				args = append(args, "-key", path.Join(kvdbVolumeInfo.mountPath, secretKeyKvdbCertKey))
			}
		} else if auth[secretKeyKvdbACLToken] != "" {
			args = append(args, "-acltoken", auth[secretKeyKvdbACLToken])
		} else if auth[secretKeyKvdbUsername] != "" && auth[secretKeyKvdbPassword] != "" {
			args = append(args, "-userpwd",
				fmt.Sprintf("%s:%s", auth[secretKeyKvdbUsername], auth[secretKeyKvdbPassword]))
		}
	}

	if t.cluster.Spec.Network != nil {
		if t.cluster.Spec.Network.DataInterface != nil &&
			*t.cluster.Spec.Network.DataInterface != "" {
			args = append(args, "-d", *t.cluster.Spec.Network.DataInterface)
		}
		if t.cluster.Spec.Network.MgmtInterface != nil &&
			*t.cluster.Spec.Network.MgmtInterface != "" {
			args = append(args, "-m", *t.cluster.Spec.Network.MgmtInterface)
		}
	}

	if t.cluster.Spec.Storage != nil {
		if t.cluster.Spec.Storage.Devices != nil {
			for _, dev := range *t.cluster.Spec.Storage.Devices {
				args = append(args, "-s", dev)
			}
		} else {
			if t.cluster.Spec.Storage.UseAllWithPartitions != nil &&
				*t.cluster.Spec.Storage.UseAllWithPartitions {
				args = append(args, "-A")
			} else if t.cluster.Spec.Storage.UseAll != nil &&
				*t.cluster.Spec.Storage.UseAll {
				args = append(args, "-a")
			}
		}
		if t.cluster.Spec.Storage.ForceUseDisks != nil &&
			*t.cluster.Spec.Storage.ForceUseDisks {
			args = append(args, "-f")
		}
		if t.cluster.Spec.Storage.JournalDevice != nil &&
			*t.cluster.Spec.Storage.JournalDevice != "" {
			args = append(args, "-j", *t.cluster.Spec.Storage.JournalDevice)
		}
		if t.cluster.Spec.Storage.SystemMdDevice != nil &&
			*t.cluster.Spec.Storage.SystemMdDevice != "" {
			args = append(args, "-metadata", *t.cluster.Spec.Storage.SystemMdDevice)
		}

	} else if t.cluster.Spec.CloudStorage != nil {
		if t.cloudConfig != nil && len(t.cloudConfig.CloudStorage) > 0 {
			// CapacitySpecs have higher preference over DeviceSpecs
			for _, cloudDriveSpec := range t.cloudConfig.CloudStorage {
				args = append(args, "-s", t.getCloudStorageArguments(cloudDriveSpec))
			}
		} else if t.cluster.Spec.CloudStorage.DeviceSpecs != nil {
			for _, dev := range *t.cluster.Spec.CloudStorage.DeviceSpecs {
				args = append(args, "-s", dev)
			}
		}
		if t.cluster.Spec.CloudStorage.JournalDeviceSpec != nil &&
			len(*t.cluster.Spec.CloudStorage.JournalDeviceSpec) > 0 {
			args = append(args, "-j", *t.cluster.Spec.CloudStorage.JournalDeviceSpec)
		}
		if t.cluster.Spec.CloudStorage.SystemMdDeviceSpec != nil &&
			len(*t.cluster.Spec.CloudStorage.SystemMdDeviceSpec) > 0 {
			args = append(args, "-metadata", *t.cluster.Spec.CloudStorage.SystemMdDeviceSpec)
		}
		if t.cluster.Spec.CloudStorage.MaxStorageNodes != nil &&
			*t.cluster.Spec.CloudStorage.MaxStorageNodes > 0 {
			args = append(args, "-max_drive_set_count",
				strconv.Itoa(int(*t.cluster.Spec.CloudStorage.MaxStorageNodes)))
		}
		if t.cloudConfig != nil && t.cloudConfig.StorageInstancesPerZone > 0 {
			args = append(args, "-max_storage_nodes_per_zone",
				strconv.Itoa(int(t.cloudConfig.StorageInstancesPerZone)))
		} else if t.cluster.Spec.CloudStorage.MaxStorageNodesPerZone != nil &&
			// cloudConfig is not generated use the max storage nodes per zone
			// provided by the user
			*t.cluster.Spec.CloudStorage.MaxStorageNodesPerZone > 0 {
			args = append(args, "-max_storage_nodes_per_zone",
				strconv.Itoa(int(*t.cluster.Spec.CloudStorage.MaxStorageNodesPerZone)))
		}
	}

	if t.cluster.Spec.SecretsProvider != nil &&
		*t.cluster.Spec.SecretsProvider != "" {
		args = append(args, "-secret_type", *t.cluster.Spec.SecretsProvider)
	}

	if t.startPort != pxutil.DefaultStartPort {
		args = append(args, "-r", strconv.Itoa(int(t.startPort)))
	}

	if t.imagePullPolicy != v1.PullAlways {
		args = append(args, "--pull", string(t.imagePullPolicy))
	}

	// --keep-px-up is default on from px-enterprise from 2.1
	if t.isPKS {
		args = append(args, "--keep-px-up")
	}

	if t.cluster.Annotations[annotationLogFile] != "" {
		args = append(args, "--log", t.cluster.Annotations[annotationLogFile])
	}

	rtOpts := make([]string, 0)
	for k, v := range t.cluster.Spec.RuntimeOpts {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		_, err := strconv.Atoi(value)
		if err != nil {
			logrus.Warnf("Invalid integer value %v in runtime options. %v", value, err)
			continue
		}
		if key != "" && value != "" {
			rtOpts = append(rtOpts, key+"="+value)
		}
	}
	if len(rtOpts) > 0 {
		args = append(args, "-rt_opts", strings.Join(rtOpts, ","))
	}

	if t.cluster.Annotations[annotationMiscArgs] != "" {
		parts, err := shlex.Split(t.cluster.Annotations[annotationMiscArgs])
		if err == nil {
			args = append(args, parts...)
		} else {
			logrus.Warnf("error parsing misc args: %v", err)
		}
	}

	return args
}

func (t *template) getEnvList() []v1.EnvVar {
	envList := []v1.EnvVar{
		{
			Name:  "PX_SECRETS_NAMESPACE",
			Value: t.cluster.Namespace,
		},
		{
			Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
			Value: "1500",
		},
		{
			Name:  "PX_TEMPLATE_VERSION",
			Value: templateVersion,
		},
	}

	if t.isPKS {
		envList = append(envList,
			v1.EnvVar{
				Name:  "PRE-EXEC",
				Value: "if [ ! -x /bin/systemctl ]; then apt-get update; apt-get install -y systemd; fi",
			},
		)
	}

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		envList = append(envList,
			v1.EnvVar{
				Name:  "CSI_ENDPOINT",
				Value: "unix://" + t.csiConfig.DriverBasePath() + "/csi.sock",
			},
		)
		if t.csiConfig.Version != "" {
			envList = append(envList,
				v1.EnvVar{
					Name:  "PORTWORX_CSIVERSION",
					Value: t.csiConfig.Version,
				},
			)
		}
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		envList = append(envList, v1.EnvVar{
			Name: "REGISTRY_CONFIG",
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					Key: ".dockerconfigjson",
					LocalObjectReference: v1.LocalObjectReference{
						Name: *t.cluster.Spec.ImagePullSecret,
					},
				},
			},
		})
	}

	for _, env := range t.cluster.Spec.Env {
		envCopy := env.DeepCopy()
		envList = append(envList, *envCopy)
	}

	return envList
}

func (t *template) getVolumeMounts() []v1.VolumeMount {
	// TODO: Imp: add etcd certs to the volume mounts
	volumeInfoList := append([]volumeInfo{}, defaultVolumeInfoList...)

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		volumeInfoList = append(volumeInfoList, volumeInfo{
			name:      "csi-driver-path",
			mountPath: t.csiConfig.DriverBasePath(),
		})
	}

	volumeMounts := make([]v1.VolumeMount, 0)
	for _, v := range volumeInfoList {
		volMount := v1.VolumeMount{
			Name:             v.name,
			MountPath:        v.mountPath,
			ReadOnly:         v.readOnly,
			MountPropagation: v.mountPropagation,
		}
		if t.isPKS && v.pks != nil && v.pks.mountPath != "" {
			volMount.MountPath = v.pks.mountPath
		}
		if volMount.MountPath != "" {
			volumeMounts = append(volumeMounts, volMount)
		}
	}

	kvdbAuth := t.loadKvdbAuth()
	if kvdbAuth[secretKeyKvdbCert] != "" {
		volumeMounts = append(volumeMounts, v1.VolumeMount{
			Name:      kvdbVolumeInfo.name,
			MountPath: kvdbVolumeInfo.mountPath,
		})
	}

	return volumeMounts
}

func (t *template) getVolumes() []v1.Volume {
	// TODO: Imp: add etcd certs to the volume list
	volumeInfoList := append([]volumeInfo{}, defaultVolumeInfoList...)

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		volumeInfoList = append(volumeInfoList, t.getCSIVolumeInfoList()...)
	}

	volumes := make([]v1.Volume, 0)
	for _, v := range volumeInfoList {
		volume := v1.Volume{
			Name: v.name,
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: v.hostPath,
					Type: v.hostPathType,
				},
			},
		}
		if t.isPKS && v.pks != nil && v.pks.hostPath != "" {
			volume.VolumeSource.HostPath.Path = v.pks.hostPath
		}
		if volume.VolumeSource.HostPath.Path != "" {
			volumes = append(volumes, volume)
		}
	}

	kvdbAuth := t.loadKvdbAuth()
	if kvdbAuth[secretKeyKvdbCert] != "" {
		kvdbVolume := v1.Volume{
			Name: kvdbVolumeInfo.name,
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName: t.cluster.Spec.Kvdb.AuthSecret,
					Items: []v1.KeyToPath{
						{
							Key:  secretKeyKvdbCert,
							Path: secretKeyKvdbCert,
						},
					},
				},
			},
		}
		if kvdbAuth[secretKeyKvdbCA] != "" {
			kvdbVolume.VolumeSource.Secret.Items = append(
				kvdbVolume.VolumeSource.Secret.Items,
				v1.KeyToPath{
					Key:  secretKeyKvdbCA,
					Path: secretKeyKvdbCA,
				},
			)
		}
		if kvdbAuth[secretKeyKvdbCertKey] != "" {
			kvdbVolume.VolumeSource.Secret.Items = append(
				kvdbVolume.VolumeSource.Secret.Items,
				v1.KeyToPath{
					Key:  secretKeyKvdbCertKey,
					Path: secretKeyKvdbCertKey,
				},
			)
		}
		volumes = append(volumes, kvdbVolume)
	}

	return volumes
}

func (t *template) getCSIVolumeInfoList() []volumeInfo {
	registrationVol := volumeInfo{
		name:         "registration-dir",
		hostPath:     "/var/lib/kubelet/plugins_registry",
		hostPathType: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
	}
	if t.csiConfig.UseOlderPluginsDirAsRegistration {
		registrationVol.hostPath = "/var/lib/kubelet/plugins"
	}

	volumeInfoList := []volumeInfo{registrationVol}
	volumeInfoList = append(volumeInfoList,
		volumeInfo{
			name:         "csi-driver-path",
			hostPath:     t.csiConfig.DriverBasePath(),
			hostPathType: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
		},
	)
	return volumeInfoList
}

func (t *template) loadKvdbAuth() map[string]string {
	if len(t.kvdb) > 0 {
		return t.kvdb
	}
	if t.cluster.Spec.Kvdb == nil {
		return nil
	}
	secretName := t.cluster.Spec.Kvdb.AuthSecret
	if secretName == "" {
		return nil
	}
	auth, err := coreops.Instance().GetSecret(secretName, t.cluster.Namespace)
	if err != nil {
		logrus.Warnf("Could not get kvdb auth secret %v/%v: %v",
			secretName, t.cluster.Namespace, err)
		return nil
	}
	t.kvdb = make(map[string]string)
	for k, v := range auth.Data {
		t.kvdb[k] = string(v)
	}
	return t.kvdb
}

func stringPtr(val string) *string {
	return &val
}

func boolPtr(val bool) *bool {
	return &val
}

func hostPathTypePtr(val v1.HostPathType) *v1.HostPathType {
	return &val
}
