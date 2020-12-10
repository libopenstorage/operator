package portworx

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/cloudops"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
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
	pxContainerName       = "portworx"
	pxKVDBContainerName   = "portworx-kvdb"
	pxAnnotationPrefix    = "portworx.io"
	templateVersion       = "v4"
	secretKeyKvdbCA       = "kvdb-ca.crt"
	secretKeyKvdbCert     = "kvdb.crt"
	secretKeyKvdbCertKey  = "kvdb.key"
	secretKeyKvdbUsername = "username"
	secretKeyKvdbPassword = "password"
	secretKeyKvdbACLToken = "acl-token"
	envKeyPXImage         = "PX_IMAGE"
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
	cluster         *corev1.StorageCluster
	isPKS           bool
	isIKS           bool
	isOpenshift     bool
	isK3s           bool
	runOnMaster     bool
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
	cluster *corev1.StorageCluster,
) (*template, error) {
	if cluster == nil {
		return nil, fmt.Errorf("storage cluster cannot be empty")
	}

	t := &template{cluster: cluster}

	var err error
	var ext string
	t.k8sVersion, ext, err = k8sutil.GetFullVersion()
	if err != nil {
		return nil, err
	}

	t.isK3s = isK3sCluster(ext)
	t.runOnMaster = t.isK3s || pxutil.RunOnMaster(cluster)
	t.pxVersion = pxutil.GetPortworxVersion(cluster)
	deprecatedCSIDriverName := pxutil.UseDeprecatedCSIDriverName(cluster)
	disableCSIAlpha := pxutil.DisableCSIAlpha(cluster)
	kubeletPath := pxutil.KubeletPath(cluster)
	csiGenerator := pxutil.NewCSIGenerator(*t.k8sVersion, *t.pxVersion,
		deprecatedCSIDriverName, disableCSIAlpha, kubeletPath)
	if pxutil.FeatureCSI.IsEnabled(cluster.Spec.FeatureGates) {
		t.csiConfig = csiGenerator.GetCSIConfiguration()
	} else {
		t.csiConfig = csiGenerator.GetBasicCSIConfiguration()
	}

	t.isPKS = pxutil.IsPKS(cluster)
	t.isIKS = pxutil.IsIKS(cluster)
	t.isOpenshift = pxutil.IsOpenshift(cluster)
	t.serviceType = pxutil.ServiceType(cluster)
	t.imagePullPolicy = pxutil.ImagePullPolicy(cluster)
	t.startPort = pxutil.StartPort(cluster)

	return t, nil
}

func (p *portworx) generateCloudStorageSpecs(
	cluster *corev1.StorageCluster,
	nodes []*corev1.StorageNode,
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

func (p *portworx) GetKVDBPodSpec(
	cluster *corev1.StorageCluster, nodeName string,
) (v1.PodSpec, error) {
	t, err := newTemplate(cluster)
	if err != nil {
		return v1.PodSpec{}, err
	}

	containers := t.kvdbContainer()
	podSpec := v1.PodSpec{
		HostNetwork:        true,
		RestartPolicy:      v1.RestartPolicyAlways,
		ServiceAccountName: pxutil.PortworxServiceAccountName(cluster),
		Containers:         []v1.Container{containers},
		NodeName:           nodeName,
	}

	if t.cluster.Spec.Placement != nil {
		if len(t.cluster.Spec.Placement.Tolerations) > 0 {
			podSpec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range t.cluster.Spec.Placement.Tolerations {
				podSpec.Tolerations = append(podSpec.Tolerations, *(toleration.DeepCopy()))
			}
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

func (p *portworx) GetStoragePodSpec(
	cluster *corev1.StorageCluster, nodeName string,
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
		ServiceAccountName: pxutil.PortworxServiceAccountName(cluster),
		Containers:         []v1.Container{containers},
		Volumes:            t.getVolumes(),
	}

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		csiRegistrar := t.csiRegistrarContainer()
		if csiRegistrar != nil {
			podSpec.Containers = append(podSpec.Containers, *csiRegistrar)
		}
	}

	if t.cluster.Spec.Placement != nil {
		if t.cluster.Spec.Placement.NodeAffinity != nil {
			podSpec.Affinity = &v1.Affinity{
				NodeAffinity: t.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}
		if len(t.cluster.Spec.Placement.Tolerations) > 0 {
			podSpec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range t.cluster.Spec.Placement.Tolerations {
				podSpec.Tolerations = append(podSpec.Tolerations, *(toleration.DeepCopy()))
			}
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

func (p *portworx) createStorageNode(cluster *corev1.StorageCluster, nodeName string, cloudConfig *cloudstorage.Config) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())

	storageNode := &corev1.StorageNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            nodeName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
			Labels:          p.GetSelectorLabels(),
		},
		Status: corev1.NodeStatus{},
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

func (p *portworx) storageNodesList(cluster *corev1.StorageCluster) ([]*corev1.StorageNode, error) {

	nodes := &corev1.StorageNodeList{}
	storageNodes := make([]*corev1.StorageNode, 0)

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

func storageNodeExists(nodeName string, nodes []*corev1.StorageNode) bool {
	for _, node := range nodes {
		if nodeName == node.Name {
			return true
		}
	}
	return false
}

func configureStorageNodeSpec(node *corev1.StorageNode, config *cloudstorage.Config) {
	node.Spec = corev1.StorageNodeSpec{CloudStorage: corev1.StorageNodeCloudDriveConfigs{}}
	for _, conf := range config.CloudStorage {
		sc := corev1.StorageNodeCloudDriveConfig{
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

func (t *template) kvdbContainer() v1.Container {
	kvdbProxyImage := util.GetImageURN(t.cluster.Spec.CustomImageRegistry, pxutil.ImageNamePause)
	kvdbTargetPort := 9019
	if t.startPort != pxutil.DefaultStartPort {
		kvdbTargetPort = t.startPort + 15
	}
	return v1.Container{
		Name:            pxKVDBContainerName,
		Image:           kvdbProxyImage,
		ImagePullPolicy: t.imagePullPolicy,
		LivenessProbe: &v1.Probe{
			PeriodSeconds:       30,
			InitialDelaySeconds: 840,
			Handler: v1.Handler{
				TCPSocket: &v1.TCPSocketAction{
					Port: intstr.FromInt(kvdbTargetPort),
					Host: "127.0.0.1",
				},
			},
		},
		ReadinessProbe: &v1.Probe{
			PeriodSeconds: 10,
			Handler: v1.Handler{
				TCPSocket: &v1.TCPSocketAction{
					Port: intstr.FromInt(kvdbTargetPort),
					Host: "127.0.0.1",
				},
			},
		},
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

	if t.cluster.Status.DesiredImages.CSINodeDriverRegistrar != "" {
		container.Name = "csi-node-driver-registrar"
		container.Image = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.cluster.Status.DesiredImages.CSINodeDriverRegistrar,
		)
		container.Args = []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			fmt.Sprintf("--kubelet-registration-path=%s/csi.sock", t.csiConfig.DriverBasePath()),
		}
	} else if t.cluster.Status.DesiredImages.CSIDriverRegistrar != "" {
		container.Name = "csi-driver-registrar"
		container.Image = util.GetImageURN(
			t.cluster.Spec.CustomImageRegistry,
			t.cluster.Status.DesiredImages.CSIDriverRegistrar,
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

	if !t.runOnMaster {
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
	}

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
		if t.cluster.Spec.Storage.KvdbDevice != nil &&
			*t.cluster.Spec.Storage.KvdbDevice != "" {
			args = append(args, "-kvdb_dev", *t.cluster.Spec.Storage.KvdbDevice)
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
		if t.cluster.Spec.CloudStorage.KvdbDeviceSpec != nil &&
			*t.cluster.Spec.CloudStorage.KvdbDeviceSpec != "" {
			args = append(args, "-kvdb_dev", *t.cluster.Spec.CloudStorage.KvdbDeviceSpec)
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

	if t.cluster.Annotations[pxutil.AnnotationLogFile] != "" {
		args = append(args, "--log", t.cluster.Annotations[pxutil.AnnotationLogFile])
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

	if parts, err := pxutil.MiscArgs(t.cluster); err == nil {
		args = append(args, parts...)
	} else {
		logrus.Warnf("error parsing misc args: %v", err)
	}

	if pxutil.EssentialsEnabled() {
		for args[len(args)-1] == "--oem" {
			args = args[:len(args)-1]
		}
		essePresent := false
		for i, arg := range args {
			if arg == "--oem" {
				args[i+1] = "esse"
				essePresent = true
				break
			}
		}
		if !essePresent {
			args = append(args, "--oem", "esse")
		}

		marketplaceName := strings.TrimSpace(os.Getenv(pxutil.EnvKeyMarketplaceName))
		if marketplaceName != "" {
			pxVer2_5_5, _ := version.NewVersion("2.5.5")
			if t.pxVersion.GreaterThanOrEqual(pxVer2_5_5) {
				args = append(args, "-marketplace_name", marketplaceName)
			}
		}
	}

	return args
}

func (t *template) getEnvList() []v1.EnvVar {
	envMap := map[string]*v1.EnvVar{
		pxutil.EnvKeyPortworxNamespace: {
			Name:  pxutil.EnvKeyPortworxNamespace,
			Value: t.cluster.Namespace,
		},
		pxutil.EnvKeyPortworxSecretsNamespace: {
			Name:  pxutil.EnvKeyPortworxSecretsNamespace,
			Value: t.cluster.Namespace,
		},
		"PX_TEMPLATE_VERSION": {
			Name:  "PX_TEMPLATE_VERSION",
			Value: templateVersion,
		},
	}

	pxVer2_6, _ := version.NewVersion("2.6")
	if t.pxVersion.LessThan(pxVer2_6) {
		envMap["AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS"] = &v1.EnvVar{
			Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
			Value: "1500",
		}
	}

	if t.isIKS {
		envMap["PX_POD_IP"] = &v1.EnvVar{
			Name: "PX_POD_IP",
			ValueFrom: &v1.EnvVarSource{
				FieldRef: &v1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		}
	}

	if t.isPKS {
		envMap["PRE-EXEC"] = &v1.EnvVar{
			Name:  "PRE-EXEC",
			Value: "if [ ! -x /bin/systemctl ]; then apt-get update; apt-get install -y systemd; fi",
		}
	}

	if t.isOpenshift {
		envMap["PRE-EXEC"] = &v1.EnvVar{
			Name:  "PRE-EXEC",
			Value: "if [ -f /etc/pwx/.private.json ] && [ -d /ostree/deploy/rhcos/deploy ]; then chattr -i /etc/pwx/.private.json /ostree/deploy/rhcos/deploy/*/etc/pwx/.private.json || /bin/true; fi",
		}
	}

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		envMap["CSI_ENDPOINT"] = &v1.EnvVar{
			Name:  "CSI_ENDPOINT",
			Value: "unix://" + t.csiConfig.DriverBasePath() + "/csi.sock",
		}

		if t.csiConfig.Version != "" {
			envMap["PORTWORX_CSIVERSION"] = &v1.EnvVar{
				Name:  "PORTWORX_CSIVERSION",
				Value: t.csiConfig.Version,
			}
		}
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		pxVer2_3_2, _ := version.NewVersion("2.3.2")
		if t.pxVersion.LessThan(pxVer2_3_2) {
			envMap["REGISTRY_CONFIG"] = &v1.EnvVar{
				Name: "REGISTRY_CONFIG",
				ValueFrom: &v1.EnvVarSource{
					SecretKeyRef: &v1.SecretKeySelector{
						Key: ".dockerconfigjson",
						LocalObjectReference: v1.LocalObjectReference{
							Name: *t.cluster.Spec.ImagePullSecret,
						},
					},
				},
			}
		} else {
			envMap["REGISTRY_SECRET"] = &v1.EnvVar{
				Name:  "REGISTRY_SECRET",
				Value: *t.cluster.Spec.ImagePullSecret,
			}
		}
	}

	// Copy user provided env and overwrite default ones with user's values
	for _, env := range t.cluster.Spec.Env {
		envMap[env.Name] = env.DeepCopy()
	}

	// Add self signed values from spec if security is enabled
	if pxutil.SecurityEnabled(t.cluster) {
		envMap[pxutil.EnvKeyPortworxAuthJwtSharedSecret] = &v1.EnvVar{
			Name: pxutil.EnvKeyPortworxAuthJwtSharedSecret,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: *t.cluster.Spec.Security.Auth.SelfSigned.SharedSecret,
					},
					Key: pxutil.SecuritySharedSecretKey,
				},
			},
		}
		envMap[pxutil.EnvKeyPortworxAuthSystemKey] = &v1.EnvVar{
			Name: pxutil.EnvKeyPortworxAuthSystemKey,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: pxutil.SecurityPXSystemSecretsSecretName,
					},
					Key: pxutil.SecuritySystemSecretKey,
				},
			},
		}
		envMap[pxutil.EnvKeyPortworxAuthJwtIssuer] = &v1.EnvVar{
			Name:  pxutil.EnvKeyPortworxAuthJwtIssuer,
			Value: *t.cluster.Spec.Security.Auth.SelfSigned.Issuer,
		}
		pxVersion := pxutil.GetPortworxVersion(t.cluster)
		storkVersion := pxutil.GetStorkVersion(t.cluster)
		pxAppsIssuerVersion, err := version.NewVersion("2.6.0")
		if err != nil {
			logrus.Errorf("failed to create PX version variable 2.6.0: %s", err.Error())
		}
		storkIssuerVersion, err := version.NewVersion("2.5.0")
		if err != nil {
			logrus.Errorf("failed to create Stork version variable 2.5.0: %s", err.Error())
		}
		// apps issuer was added in PX version 2.6.0
		if pxVersion.GreaterThanOrEqual(pxAppsIssuerVersion) && storkVersion.GreaterThanOrEqual(storkIssuerVersion) {
			envMap[pxutil.EnvKeyPortworxAuthSystemAppsKey] = &v1.EnvVar{
				Name: pxutil.EnvKeyPortworxAuthSystemAppsKey,
				ValueFrom: &v1.EnvVarSource{
					SecretKeyRef: &v1.SecretKeySelector{
						LocalObjectReference: v1.LocalObjectReference{
							Name: pxutil.SecurityPXSystemSecretsSecretName,
						},
						Key: pxutil.SecurityAppsSecretKey,
					},
				},
			}
		} else {
			// otherwise, use the stork issuer for pre-2.6 support
			envMap[pxutil.EnvKeyPortworxAuthStorkKey] = &v1.EnvVar{
				Name: pxutil.EnvKeyPortworxAuthStorkKey,
				ValueFrom: &v1.EnvVarSource{
					SecretKeyRef: &v1.SecretKeySelector{
						LocalObjectReference: v1.LocalObjectReference{
							Name: pxutil.SecurityPXSystemSecretsSecretName,
						},
						Key: pxutil.SecurityAppsSecretKey,
					},
				},
			}
		}
	}

	envList := make([]v1.EnvVar, 0)
	for _, env := range envMap {
		envList = append(envList, *env)
	}
	return envList
}

func (t *template) getVolumeMounts() []v1.VolumeMount {
	volumeInfoList := append([]volumeInfo{}, defaultVolumeInfoList...)
	if t.isK3s {
		volumeInfoList = append(volumeInfoList, t.getK3sVolumeInfoList()...)
	}

	volumeMounts := make([]v1.VolumeMount, 0, len(volumeInfoList))
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
	volumeInfoList := append([]volumeInfo{}, defaultVolumeInfoList...)

	if pxutil.FeatureCSI.IsEnabled(t.cluster.Spec.FeatureGates) {
		volumeInfoList = append(volumeInfoList, t.getCSIVolumeInfoList()...)
	}
	if t.isK3s {
		volumeInfoList = append(volumeInfoList, t.getK3sVolumeInfoList()...)
	}

	volumes := make([]v1.Volume, 0, len(volumeInfoList))
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
	kubeletPath := pxutil.KubeletPath(t.cluster)
	registrationVol := volumeInfo{
		name:         "registration-dir",
		hostPath:     kubeletPath + "/plugins_registry",
		hostPathType: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
	}
	if t.csiConfig.UseOlderPluginsDirAsRegistration {
		registrationVol.hostPath = kubeletPath + "/plugins"
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

func (t *template) getK3sVolumeInfoList() []volumeInfo {
	return []volumeInfo{
		{
			name:      "containerd-k3s",
			hostPath:  "/run/k3s/containerd/containerd.sock",
			mountPath: "/run/containerd/containerd.sock",
		},
	}
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

func guestAccessTypePtr(val corev1.GuestAccessType) *corev1.GuestAccessType {
	return &val
}

func isK3sCluster(ext string) bool {
	if len(ext) > 0 {
		return strings.HasPrefix(ext[1:], "k3s")
	}
	return false
}
