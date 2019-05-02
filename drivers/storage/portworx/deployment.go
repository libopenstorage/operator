package portworx

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/google/shlex"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	pxContainerName          = "portworx"
	pxServiceAccount         = "px-account"
	pxAnnotationPrefix       = "portworx.io"
	annotationIsPKS          = pxAnnotationPrefix + "/is-pks"
	annotationIsOpenshift    = pxAnnotationPrefix + "/is-openshift"
	annotationLogFile        = pxAnnotationPrefix + "/log-file"
	annotationCustomRegistry = pxAnnotationPrefix + "/custom-registry"
	annotationCSIVersion     = pxAnnotationPrefix + "/csi-version"
	annotationMiscArgs       = pxAnnotationPrefix + "/misc-args"
	templateVersion          = "v4"
	csiBasePath              = "/var/lib/kubelet/plugins/com.openstorage.pxd"
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
	// commonDockerRegistries is a map of commonly used Docker registries
	commonDockerRegistries = map[string]bool{
		"docker.io":                   true,
		"index.docker.io":             true,
		"registry-1.docker.io":        true,
		"registry.connect.redhat.com": true,
	}

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

	// csiVolumeInfoList is a list of volumes applicable when running csi sidecars
	csiVolumeInfoList = []volumeInfo{
		{
			name:         "registration-dir",
			hostPath:     "/var/lib/kubelet/plugins_registry",
			hostPathType: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
		},
		{
			name:         "csi-driver-path",
			hostPath:     "/var/lib/kubelet/plugins/com.openstorage.pxd",
			hostPathType: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
		},
	}
)

type template struct {
	cluster         *corev1alpha1.StorageCluster
	isPKS           bool
	isOpenshift     bool
	imagePullPolicy v1.PullPolicy
	startPort       int
}

func newTemplate(cluster *corev1alpha1.StorageCluster) (*template, error) {
	if cluster == nil {
		return nil, fmt.Errorf("storage cluster cannot be empty")
	}

	t := &template{cluster: cluster}

	enabled, err := strconv.ParseBool(cluster.Annotations[annotationIsPKS])
	t.isPKS = err == nil && enabled

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationIsOpenshift])
	t.isOpenshift = err == nil && enabled

	t.imagePullPolicy = v1.PullAlways
	if cluster.Spec.ImagePullPolicy == v1.PullNever ||
		cluster.Spec.ImagePullPolicy == v1.PullIfNotPresent {
		t.imagePullPolicy = cluster.Spec.ImagePullPolicy
	}

	t.startPort = defaultStartPort
	if cluster.Spec.StartPort != nil {
		t.startPort = int(*cluster.Spec.StartPort)
	}

	return t, nil
}

// TODO [Imp] Validate the cluster spec and return errors in the configuration
func (p *portworx) GetStoragePodSpec(cluster *corev1alpha1.StorageCluster) v1.PodSpec {
	t, err := newTemplate(cluster)
	if err != nil {
		return v1.PodSpec{}
	}

	podSpec := v1.PodSpec{
		HostNetwork:        true,
		RestartPolicy:      v1.RestartPolicyAlways,
		ServiceAccountName: pxServiceAccount,
		Containers:         []v1.Container{t.portworxContainer()},
		Volumes:            t.getVolumes(),
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

	return podSpec
}

func (t *template) portworxContainer() v1.Container {
	imageRegistry := t.cluster.Annotations[annotationCustomRegistry]

	return v1.Container{
		Name:            pxContainerName,
		Image:           getImageURN(imageRegistry, t.cluster.Spec.Image),
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
		if t.cluster.Spec.CloudStorage.DeviceSpecs != nil {
			for _, dev := range *t.cluster.Spec.CloudStorage.DeviceSpecs {
				args = append(args, "-s", dev)
			}
		}
		if t.cluster.Spec.CloudStorage.JournalDeviceSpec != nil &&
			*t.cluster.Spec.CloudStorage.JournalDeviceSpec != "" {
			args = append(args, "-j", *t.cluster.Spec.CloudStorage.JournalDeviceSpec)
		}
		if t.cluster.Spec.CloudStorage.SystemMdDeviceSpec != nil &&
			*t.cluster.Spec.CloudStorage.SystemMdDeviceSpec != "" {
			args = append(args, "-metadata", *t.cluster.Spec.CloudStorage.SystemMdDeviceSpec)
		}
		if t.cluster.Spec.CloudStorage.MaxStorageNodes != nil {
			args = append(args, "-max_drive_set_count",
				strconv.Itoa(int(*t.cluster.Spec.CloudStorage.MaxStorageNodes)))
		}
		if t.cluster.Spec.CloudStorage.MaxStorageNodesPerZone != nil {
			args = append(args, "-max_storage_nodes_per_zone",
				strconv.Itoa(int(*t.cluster.Spec.CloudStorage.MaxStorageNodesPerZone)))
		}
	}

	if t.cluster.Spec.SecretsProvider != nil &&
		*t.cluster.Spec.SecretsProvider != "" {
		args = append(args, "-secret_type", *t.cluster.Spec.SecretsProvider)
	}

	if t.startPort != defaultStartPort {
		args = append(args, "-r", strconv.Itoa(int(t.startPort)))
	}

	if t.imagePullPolicy != v1.PullAlways {
		args = append(args, "--pull", string(t.imagePullPolicy))
	}

	if t.cluster.Annotations[annotationCSIVersion] != "" {
		args = append(args, "--csiversion", t.cluster.Annotations[annotationCSIVersion])
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
			Name:  "AUTO_NODE_RECOVERY_TIMEOUT_IN_SECS",
			Value: "1500",
		},
		{
			Name:  "PX_TEMPLATE_VERSION",
			Value: templateVersion,
		},
	}

	if t.isPKS {
		envList = append(envList, v1.EnvVar{
			Name:  "PRE-EXEC",
			Value: "if [ ! -x /bin/systemctl ]; then apt-get update; apt-get install -y systemd; fi",
		})
	}

	if FeatureCSI.isEnabled(t.cluster.Spec.FeatureGates) {
		envList = append(envList, v1.EnvVar{
			Name:  "CSI_ENDPOINT",
			Value: "unix://" + csiBasePath + "/csi.sock",
		})
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		envList = append(envList, v1.EnvVar{
			Name: "REGISTRY_CONFIG",
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					// TODO: for older versions of k8s it could be .dockercfg
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

	if FeatureCSI.isEnabled(t.cluster.Spec.FeatureGates) {
		volumeInfoList = append(volumeInfoList, csiVolumeInfoList...)
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
	return volumeMounts
}

func (t *template) getVolumes() []v1.Volume {
	// TODO: Imp: add etcd certs to the volume list
	volumeInfoList := append([]volumeInfo{}, defaultVolumeInfoList...)

	if FeatureCSI.isEnabled(t.cluster.Spec.FeatureGates) {
		volumeInfoList = append(volumeInfoList, csiVolumeInfoList...)
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
	return volumes
}

// getImageURN returns the docker image URN depending on a given registryAndRepo and given image
func getImageURN(registryAndRepo, image string) string {
	if image == "" {
		return ""
	}

	registryAndRepo = strings.TrimRight(registryAndRepo, "/")
	if registryAndRepo == "" {
		// no registry/repository specifed, return image
		return image
	}

	imgParts := strings.Split(image, "/")
	if len(imgParts) > 1 {
		// advance imgParts to swallow the common registry
		if _, present := commonDockerRegistries[imgParts[0]]; present {
			imgParts = imgParts[1:]
		}
	}

	// if we have '/' in the registryAndRepo, return <registry/repository/><only-image>
	// else (registry only) -- return <registry/><image-with-repository>
	if strings.Contains(registryAndRepo, "/") && len(imgParts) > 1 {
		// advance to the last element, skipping image's repository
		imgParts = imgParts[len(imgParts)-1:]
	}
	return registryAndRepo + "/" + path.Join(imgParts...)
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
