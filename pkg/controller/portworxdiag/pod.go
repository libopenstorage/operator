package portworxdiag

import (
	"fmt"
	"os"

	"github.com/libopenstorage/operator/drivers/storage/portworx"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	portworxv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	PortworxDiagLabel = "portworx-diag"
)

func v1Volume(name, path string, hpType v1.HostPathType) v1.Volume {
	return v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: path,
				Type: &hpType,
			},
		},
	}
}

func volumes() []v1.Volume {
	return []v1.Volume{
		v1Volume("dockersock", "/var/run/docker.sock", v1.HostPathUnset),
		v1Volume("containerddir", "/run/containerd", v1.HostPathUnset),
		v1Volume("criosock", "/var/run/crio", v1.HostPathUnset),
		v1Volume("crioconf", "/etc/crictl.yaml", v1.HostPathFileOrCreate),
		v1Volume("optpwx", "/opt/pwx", v1.HostPathUnset),
		v1Volume("procmount", "/proc", v1.HostPathUnset),
		v1Volume("sysdmount", "/etc/systemd/system", v1.HostPathUnset),
		v1Volume("dbusmount", "/var/run/dbus", v1.HostPathUnset),
		v1Volume("varlibosd", "/var/lib/osd", v1.HostPathUnset),
		v1Volume("diagsdump", "/var/cores", v1.HostPathUnset),
		v1Volume("etcpwx", "/etc/pwx", v1.HostPathUnset),
		v1Volume("journalmount1", "/var/run/log", v1.HostPathUnset),
		v1Volume("journalmount2", "/var/log", v1.HostPathUnset),
		v1Volume("registration-dir", "/var/lib/kubelet/plugins_registry", v1.HostPathDirectoryOrCreate),
		v1Volume("csi-driver-path", "/var/lib/kubelet/plugins/pxd.portworx.com", v1.HostPathDirectoryOrCreate),
	}
}

func volumeMounts() []v1.VolumeMount {
	mp := v1.MountPropagationBidirectional
	return []v1.VolumeMount{
		{Name: "dockersock", MountPath: "/var/run/docker.sock"},
		{Name: "containerddir", MountPath: "/run/containerd"},
		{Name: "criosock", MountPath: "/var/run/crio"},
		{Name: "crioconf", MountPath: "/etc/crictl.yaml"},
		{Name: "optpwx", MountPath: "/opt/pwx"},
		{Name: "procmount", MountPath: "/host_proc"},
		{Name: "sysdmount", MountPath: "/etc/systemd/system"},
		{Name: "dbusmount", MountPath: "/var/run/dbus"},
		{Name: "varlibosd", MountPath: "/var/lib/osd", MountPropagation: &mp},
		{Name: "diagsdump", MountPath: "/var/cores"},
		{Name: "etcpwx", MountPath: "/etc/pwx"},
		{Name: "journalmount1", MountPath: "/var/run/log", ReadOnly: true},
		{Name: "journalmount2", MountPath: "/var/log", ReadOnly: true},
	}
}

func makeDiagPodTemplate(cluster *corev1.StorageCluster, diag *portworxv1.PortworxDiag, ns string, nodeName string, nodeID string) (*v1.PodTemplateSpec, error) {
	svcLinks := true
	terminationGP := int64(10)
	privileged := true

	diagImage := fmt.Sprintf("%s:master", portworx.DefaultPortworxImage)
	if img := os.Getenv("DIAG_IMAGE"); len(img) > 0 {
		diagImage = img
	}
	diagImageURN := util.GetImageURN(cluster, diagImage)

	isController := false
	blockOwnerDeletion := true

	podTemplateSpec := &v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "px-diag",
			Namespace:    ns,
			Labels: map[string]string{
				"name":                           PortworxDiagLabel,
				portworxv1.LabelPortworxDiagName: diag.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "portworx.io/v1",
					Kind:               "PortworxDiag",
					Name:               diag.Name,
					Controller:         &isController,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
		},
		Spec: v1.PodSpec{
			NodeName:                      nodeName,
			HostPID:                       true,                      // We *do* need this
			HostNetwork:                   true,                      // Do we need this?: https://portworx.atlassian.net/browse/PWX-32177
			RestartPolicy:                 v1.RestartPolicyOnFailure, //
			DNSPolicy:                     v1.DNSClusterFirst,        // Do we need this? https://portworx.atlassian.net/browse/PWX-32177
			EnableServiceLinks:            &svcLinks,                 // Do we need this? https://portworx.atlassian.net/browse/PWX-32177
			ServiceAccountName:            pxutil.PortworxServiceAccountName(cluster),
			TerminationGracePeriodSeconds: &terminationGP,
			Volumes:                       volumes(),
			Containers: []v1.Container{
				{
					Name:            "px-diag-collector",
					Image:           diagImageURN,
					ImagePullPolicy: v1.PullAlways,
					Args: []string{
						"--diags",
						"--diags-obj-name",
						diag.Name,
						"--diags-obj-namespace",
						diag.Namespace,
						"--diags-node-id",
						nodeID,
					},
					SecurityContext: &v1.SecurityContext{
						Privileged: &privileged,
					},
					VolumeMounts: volumeMounts(),
				},
			},
		},
	}
	if cluster.Spec.ImagePullSecret != nil && *cluster.Spec.ImagePullSecret != "" {
		podTemplateSpec.Spec.ImagePullSecrets = []v1.LocalObjectReference{
			{Name: *cluster.Spec.ImagePullSecret},
		}
	}
	k8sutil.AddOrUpdateStoragePodTolerations(&podTemplateSpec.Spec)
	return podTemplateSpec, nil
}
