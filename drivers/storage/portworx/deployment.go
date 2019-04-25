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
	annotationIsDocker       = "portworx/is-docker"
	annotationIsPxDev        = "portworx/is-px-dev"
	annotationRunOnMaster    = "portworx/run-on-master"
	annotationIsPKS          = "portworx/is-pks"
	annotationIsCoreOS       = "portworx/is-coreos"
	annotationIsOpenshift    = "portworx/is-openshift"
	annotationLogFile        = "portworx/log-file"
	annotationCustomRegistry = "portworx/custom-registry"
	annotationCSIVersion     = "portworx/csi-version"
	annotationMiscArgs       = "portworx/misc-args"
	templateVersion          = "v4"
	defaultStartPort         = 9001
	csiBasePath              = "/var/lib/kubelet/plugins/com.openstorage.pxd"
	masterTaint              = "node-role.kubernetes.io/master"
)

var (
	// commonDockerRegistries is a map of commonly used Docker registries
	commonDockerRegistries = map[string]bool{
		"docker.io":                   true,
		"index.docker.io":             true,
		"registry-1.docker.io":        true,
		"registry.connect.redhat.com": true,
	}
)

type template struct {
	cluster         *corev1alpha1.StorageCluster
	isDocker        bool
	isPxDev         bool
	runOnMaster     bool
	isPKS           bool
	isCoreOS        bool
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

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationIsCoreOS])
	t.isCoreOS = err == nil && enabled

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationIsOpenshift])
	t.isOpenshift = err == nil && enabled

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationIsDocker])
	t.isDocker = err == nil && enabled

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationIsPxDev])
	t.isPxDev = err == nil && enabled

	enabled, err = strconv.ParseBool(cluster.Annotations[annotationRunOnMaster])
	t.runOnMaster = err == nil && enabled

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

	podSpec.Affinity = &v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      "px/enabled",
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"false"},
							},
						},
					},
				},
			},
		},
	}

	if t.cluster.Spec.ImagePullSecret != nil && *t.cluster.Spec.ImagePullSecret != "" {
		podSpec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *t.cluster.Spec.ImagePullSecret,
			},
		)
	}

	if t.isPxDev || t.runOnMaster {
		podSpec.Tolerations = []v1.Toleration{
			{
				Key:    masterTaint,
				Effect: v1.TaintEffectNoSchedule,
			},
		}
	}

	return podSpec
}

func (t *template) portworxContainer() v1.Container {
	imageRegistry := t.cluster.Annotations[annotationCustomRegistry]

	readinessProbe := &v1.Probe{
		PeriodSeconds: 10,
		Handler: v1.Handler{
			HTTPGet: &v1.HTTPGetAction{
				Host: "127.0.0.1",
				Path: "/health",
				Port: intstr.FromInt(t.startPort + 14),
			},
		},
	}
	if t.isDocker {
		readinessProbe.Handler.HTTPGet.Path = "/v1/cluster/nodehealth"
		readinessProbe.Handler.HTTPGet.Port = intstr.FromInt(t.startPort)
	}

	isPrivileged := true

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
		ReadinessProbe:         readinessProbe,
		TerminationMessagePath: "/tmp/px-termination-log",
		SecurityContext: &v1.SecurityContext{
			Privileged: &isPrivileged,
		},
		VolumeMounts: t.getVolumeMounts(),
	}
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
		if t.cluster.Spec.Storage.JournalDevice != nil &&
			*t.cluster.Spec.Storage.JournalDevice != "" {
			args = append(args, "-j", *t.cluster.Spec.Storage.JournalDevice)
		}
		if t.cluster.Spec.Storage.SystemMdDevice != nil &&
			*t.cluster.Spec.Storage.SystemMdDevice != "" {
			args = append(args, "-metadata", *t.cluster.Spec.Storage.SystemMdDevice)
		}
	}

	if t.cluster.Spec.CloudStorage != nil {
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

	if t.isPxDev {
		args = append(args, "--dev")
	}

	if t.isPKS {
		args = append(args, "--keep-px-up")
	}

	if t.cluster.Annotations[annotationLogFile] != "" {
		args = append(args, "--log", t.cluster.Annotations[annotationLogFile])
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

	if CSI.isEnabled(t.cluster.Spec.FeatureGates) {
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
	volumeMounts := []v1.VolumeMount{
		{
			Name:      "diagsdump",
			MountPath: "/var/cores",
		},
		{
			Name:      "dockersock",
			MountPath: "/var/run/docker.sock",
		},
		{
			Name:      "containerdsock",
			MountPath: "/run/containerd",
		},
		{
			Name:      "etcpwx",
			MountPath: "/etc/pwx",
		},
	}

	if !t.isDocker {
		volumeMounts = append(volumeMounts,
			v1.VolumeMount{
				Name:      "optpwx",
				MountPath: "/opt/pwx",
			},
			v1.VolumeMount{
				Name:      "procmount",
				MountPath: "/host_proc",
			},
			v1.VolumeMount{
				Name:      "sysdmount",
				MountPath: "/etc/systemd/system",
			},
			v1.VolumeMount{
				Name:      "journalmount1",
				MountPath: "/var/run/log",
				ReadOnly:  true,
			},
			v1.VolumeMount{
				Name:      "journalmount2",
				MountPath: "/var/log",
				ReadOnly:  true,
			},
			v1.VolumeMount{
				Name:      "dbusmount",
				MountPath: "/var/run/dbus",
			},
		)

		if t.isPKS {
			volumeMounts = append(volumeMounts,
				v1.VolumeMount{
					Name:      "pxlogs",
					MountPath: "/var/lib/osd/log",
				},
			)
		}
	} else {
		// TODO: older versions of k8s don't support mountPropagation
		// Use "/mount/path:shared" as mount path instead
		mountPropagation := v1.MountPropagationBidirectional
		volumeMounts = append(volumeMounts,
			v1.VolumeMount{
				Name:      "dev",
				MountPath: "/dev",
			},
			v1.VolumeMount{
				Name:      "optpwx",
				MountPath: "/export_bin",
			},
			v1.VolumeMount{
				Name:      "dockerplugins",
				MountPath: "/run/docker/plugins",
			},
			v1.VolumeMount{
				Name:             "libosd",
				MountPath:        "/var/lib/osd",
				MountPropagation: &mountPropagation,
			},
			v1.VolumeMount{
				Name:      "hostproc",
				MountPath: "/hostproc",
			},
		)

		srcMount := v1.VolumeMount{
			Name:      "src",
			MountPath: "/usr/src",
		}
		if t.isCoreOS {
			srcMount.MountPath = "/lib/modules"
		}

		kubeletMount := v1.VolumeMount{
			Name:             "kubelet",
			MountPath:        "/var/lib/kubelet",
			MountPropagation: &mountPropagation,
		}
		if t.isOpenshift {
			kubeletMount.MountPath = "/var/lib/origin/openshift.local.volumes"
		}

		volumeMounts = append(volumeMounts, srcMount, kubeletMount)
	}

	// TODO: Imp: add etcd certs to the volume mounts
	return volumeMounts
}

func (t *template) getVolumes() []v1.Volume {
	volumeMap := map[string]string{
		"diagsdump":      "/var/cores",
		"dockersock":     "/var/run/docker.sock",
		"containerdsock": "/run/containerd",
		"etcpwx":         "/etc/pwx",
	}
	if t.isPKS {
		volumeMap["diagsdump"] = "/var/vcap/stores/cores"
		volumeMap["dockersock"] = "/var/vcap/sys/run/docker/docker.sock"
		volumeMap["containerdsock"] = "/var/vcap/sys/run/containerd"
		volumeMap["etcpwx"] = "/var/vcap/store/etc/pwx"
		volumeMap["pxlogs"] = "/var/vcap/store/lib/osd/log"
	}

	volumes := make([]v1.Volume, 0)
	for name, path := range volumeMap {
		volumes = append(volumes,
			v1.Volume{
				Name: name,
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: path,
					},
				},
			},
		)
	}

	if !t.isDocker {
		runcVolumeMap := map[string]string{
			"procmount":     "/proc",
			"sysdmount":     "/etc/systemd/system",
			"journalmount1": "/var/run/log",
			"journalmount2": "/var/log",
			"dbusmount":     "/var/run/dbus",
		}
		for name, path := range runcVolumeMap {
			volumes = append(volumes,
				v1.Volume{
					Name: name,
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: path,
						},
					},
				},
			)
		}

		optpwxVolume := v1.Volume{
			Name: "optpwx",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/opt/pwx",
				},
			},
		}
		if t.isPKS {
			optpwxVolume.VolumeSource.HostPath.Path = "/var/vcap/store/opt/pwx"
		}
		volumes = append(volumes, optpwxVolume)

	} else {
		dockerVolumeMap := map[string]string{
			"dev":           "/dev",
			"optpwx":        "/opt/pwx/bin",
			"cores":         "/var/cores",
			"dockerplugins": "/run/docker/plugins",
			"libosd":        "/var/lib/osd",
			"hostproc":      "/proc",
		}
		for name, path := range dockerVolumeMap {
			volumes = append(volumes,
				v1.Volume{
					Name: name,
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: path,
						},
					},
				},
			)
		}

		srcVolume := v1.Volume{
			Name: "src",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/usr/src",
				},
			},
		}
		if t.isCoreOS {
			srcVolume.VolumeSource.HostPath.Path = "/lib/modules"
		}

		kubeletVolume := v1.Volume{
			Name: "kubelet",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/var/lib/kubelet",
				},
			},
		}
		if t.isOpenshift {
			kubeletVolume.VolumeSource.HostPath.Path = "/var/lib/origin/openshift.local.volumes"
		}

		volumes = append(volumes, srcVolume, kubeletVolume)
	}

	if CSI.isEnabled(t.cluster.Spec.FeatureGates) {
		hostPathType := v1.HostPathDirectoryOrCreate
		volumes = append(volumes,
			v1.Volume{
				Name: "registration-dir",
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: "/var/lib/kubelet/plugins_registry",
						Type: &hostPathType,
					},
				},
			},
			v1.Volume{
				Name: "csi-driver-path",
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: "/var/lib/kubelet/plugins/com.openstorage.pxd",
						Type: &hostPathType,
					},
				},
			},
		)
	}

	// TODO: Imp: add etcd certs to the volume mounts
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
