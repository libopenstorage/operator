package portworx

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/portworx/kvdb"
	"github.com/portworx/kvdb/consul"
	e2 "github.com/portworx/kvdb/etcd/v2"
	e3 "github.com/portworx/kvdb/etcd/v3"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	configMapNameRegex = regexp.MustCompile("[^a-zA-Z0-9]+")
	getKVDBVersion     = kvdb.Version
	newKVDB            = kvdb.New
)

const (
	dsOptPwxVolumeName                = "optpwx"
	dsSockPwxVolumeName               = "sockpwx"
	dsEtcPwxVolumeName                = "etcpwx"
	dsHostProcVolumeName              = "hostproc"
	dsDbusVolumeName                  = "dbus"
	dsSysdVolumeName                  = "sysdmount"
	dsDevVolumeName                   = "dev"
	dsMultipathVolumeName             = "etc-multipath"
	dsLvmVolumeName                   = "lvm"
	dsSysVolumeName                   = "sys"
	dsUdevVolumeName                  = "run-udev-data"
	sysdmount                         = "/etc/systemd/system"
	devMount                          = "/dev"
	multipathMount                    = "/etc/multipath"
	lvmMount                          = "/run/lvm"
	sysMount                          = "/sys"
	udevMount                         = "/run/udev/data"
	dbusPath                          = "/var/run/dbus"
	pksPersistentStoreRoot            = "/var/vcap/store"
	pxOptPwx                          = "/opt/pwx"
	pxEtcPwx                          = "/etc/pwx"
	pxSockPwx                         = "/var/lib/osd/driver"
	pxNodeWiperServiceAccountName     = "px-node-wiper"
	pxNodeWiperClusterRoleName        = "px-node-wiper"
	pxNodeWiperClusterRoleBindingName = "px-node-wiper"
	pxNodeWiperDaemonSetName          = "px-node-wiper"
	pxKvdbPrefix                      = "pwx/"
	internalEtcdConfigMapPrefix       = "px-bootstrap-"
	cloudDriveConfigMapPrefix         = "px-cloud-drive-"
	bootstrapCloudDriveNamespace      = "kube-system"
)

// UninstallPortworx provides a set of APIs to uninstall portworx
type UninstallPortworx interface {
	// RunNodeWiper runs the node-wiper daemonset
	RunNodeWiper(removeData bool, recorder record.EventRecorder) error
	// GetNodeWiperStatus returns the status of the node-wiper daemonset
	// returns the no. of completed, in progress and total pods
	GetNodeWiperStatus() (int32, int32, int32, error)
	// DeleteNodeWiper deletes the node-wiper daemonset
	DeleteNodeWiper() error
	// WipeMetadata wipes the metadata associated with Portworx cluster
	WipeMetadata() error
}

// NewUninstaller returns an implementation of UninstallPortworx interface
func NewUninstaller(
	cluster *corev1.StorageCluster,
	k8sClient client.Client,
) UninstallPortworx {
	return &uninstallPortworx{
		cluster:   cluster,
		k8sClient: k8sClient,
	}
}

type uninstallPortworx struct {
	cluster   *corev1.StorageCluster
	k8sClient client.Client
}

func (u *uninstallPortworx) GetNodeWiperStatus() (int32, int32, int32, error) {
	ds := &appsv1.DaemonSet{}
	err := u.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      pxNodeWiperDaemonSetName,
			Namespace: u.cluster.Namespace,
		},
		ds,
	)
	if err != nil {
		return -1, -1, -1, err
	}

	pods, err := k8sutil.GetDaemonSetPods(u.k8sClient, ds)
	if err != nil {
		return -1, -1, -1, err
	}
	totalPods := ds.Status.DesiredNumberScheduled
	completedPods := 0
	for _, pod := range pods {
		if len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].Ready {
			completedPods++
		}
	}
	logrus.Infof("Node Wiper Status: Completed [%v] InProgress [%v] Total Pods [%v]", completedPods, totalPods-int32(completedPods), totalPods)
	return int32(completedPods), totalPods - int32(completedPods), totalPods, nil
}

func (u *uninstallPortworx) WipeMetadata() error {
	strippedClusterName := strings.ToLower(configMapNameRegex.ReplaceAllString(u.cluster.Name, ""))

	configMaps := []string{
		fmt.Sprintf("%s%s", internalEtcdConfigMapPrefix, strippedClusterName),
		fmt.Sprintf("%s%s", cloudDriveConfigMapPrefix, strippedClusterName),
	}
	for _, cm := range configMaps {
		err := k8sutil.DeleteConfigMap(u.k8sClient, cm, bootstrapCloudDriveNamespace)
		if err != nil {
			return err
		}
	}
	if u.cluster.Spec.Kvdb.Internal {
		// no more work needed
		return nil
	}

	if len(u.cluster.Spec.Kvdb.AuthSecret) != 0 {
		// TODO: Add support for wiping authenticated kvdb
		logrus.Warnf("Wiping kvdb data from an authenticated kvdb not supported")
		return nil
	}
	kv, err := getKVDBClient(u.cluster.Spec.Kvdb.Endpoints, nil)
	if err != nil {
		logrus.Warnf("Failed to create a kvdb client for %v", u.cluster.Spec.Kvdb.Endpoints)
		return err
	}
	return kv.DeleteTree(u.cluster.Name)
}

func (u *uninstallPortworx) RunNodeWiper(
	removeData bool,
	recorder record.EventRecorder,
) error {
	pwxHostPathRoot := "/"

	enabled, err := strconv.ParseBool(u.cluster.Annotations[pxutil.AnnotationIsPKS])
	isPKS := err == nil && enabled

	if isPKS {
		pwxHostPathRoot = pksPersistentStoreRoot
	}

	trueVar := true
	labels := map[string]string{
		"name": pxNodeWiperDaemonSetName,
	}

	wiperImage := k8sutil.GetValueFromEnv(envKeyNodeWiperImage, u.cluster.Spec.Env)
	if len(wiperImage) == 0 {
		release := manifest.Instance().GetVersions(u.cluster, true)
		wiperImage = release.Components.NodeWiper
	}
	wiperImage = util.GetImageURN(u.cluster.Spec.CustomImageRegistry, wiperImage)

	args := []string{"-w"}
	if removeData {
		args = append(args, "-r")
	}

	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())

	err = u.createServiceAccount(ownerRef)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	err = u.createClusterRole()
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	err = u.createClusterRoleBinding()
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pxNodeWiperDaemonSetName,
			Namespace:       u.cluster.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:            pxNodeWiperDaemonSetName,
							Image:           wiperImage,
							ImagePullPolicy: pxutil.ImagePullPolicy(u.cluster),
							Args:            args,
							SecurityContext: &v1.SecurityContext{
								Privileged: &trueVar,
							},
							ReadinessProbe: &v1.Probe{
								InitialDelaySeconds: 30,
								Handler: v1.Handler{
									Exec: &v1.ExecAction{
										Command: []string{"cat", "/tmp/px-node-wipe-done"},
									},
								},
							},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      dsEtcPwxVolumeName,
									MountPath: pxEtcPwx,
								},
								{
									Name:      dsHostProcVolumeName,
									MountPath: "/hostproc",
								},
								{
									Name:      dsOptPwxVolumeName,
									MountPath: pxOptPwx,
								},
								{
									Name:      dsSockPwxVolumeName,
									MountPath: pxSockPwx,
								},
								{
									Name:      dsDbusVolumeName,
									MountPath: dbusPath,
								},
								{
									Name:      dsSysdVolumeName,
									MountPath: sysdmount,
								},
								{
									Name:      dsDevVolumeName,
									MountPath: devMount,
								},
								{
									Name:      dsLvmVolumeName,
									MountPath: lvmMount,
								},
								{
									Name:      dsMultipathVolumeName,
									MountPath: multipathMount,
								},
								{
									Name:      dsUdevVolumeName,
									MountPath: udevMount,
									ReadOnly:  true,
								},
								{
									Name:      dsSysVolumeName,
									MountPath: sysMount,
								},
							},
						},
					},
					RestartPolicy:      "Always",
					ServiceAccountName: pxNodeWiperServiceAccountName,
					Volumes: []v1.Volume{
						{
							Name: dsEtcPwxVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: path.Join(pwxHostPathRoot, pxEtcPwx),
								},
							},
						},
						{
							Name: dsHostProcVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: "/proc",
								},
							},
						},
						{
							Name: dsOptPwxVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: path.Join(pwxHostPathRoot, pxOptPwx),
								},
							},
						},
						{
							Name: dsSockPwxVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: path.Join(pwxHostPathRoot + pxSockPwx),
								},
							},
						},
						{
							Name: dsDbusVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: dbusPath,
								},
							},
						},
						{
							Name: dsSysdVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: sysdmount,
								},
							},
						},
						{
							Name: dsDevVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: devMount,
								},
							},
						},
						{
							Name: dsMultipathVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: multipathMount,
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: dsLvmVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: lvmMount,
									Type: hostPathTypePtr(v1.HostPathDirectoryOrCreate),
								},
							},
						},
						{
							Name: dsUdevVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: udevMount,
								},
							},
						},
						{
							Name: dsSysVolumeName,
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{
									Path: sysMount,
								},
							},
						},
					},
				},
			},
		},
	}

	if u.cluster.Spec.ImagePullSecret != nil && *u.cluster.Spec.ImagePullSecret != "" {
		ds.Spec.Template.Spec.ImagePullSecrets = append(
			[]v1.LocalObjectReference{},
			v1.LocalObjectReference{
				Name: *u.cluster.Spec.ImagePullSecret,
			},
		)
	}

	if u.cluster.Spec.Placement != nil {
		if u.cluster.Spec.Placement.NodeAffinity != nil {
			ds.Spec.Template.Spec.Affinity = &v1.Affinity{
				NodeAffinity: u.cluster.Spec.Placement.NodeAffinity.DeepCopy(),
			}
		}

		if len(u.cluster.Spec.Placement.Tolerations) > 0 {
			ds.Spec.Template.Spec.Tolerations = make([]v1.Toleration, 0)
			for _, toleration := range u.cluster.Spec.Placement.Tolerations {
				ds.Spec.Template.Spec.Tolerations = append(
					ds.Spec.Template.Spec.Tolerations,
					*(toleration.DeepCopy()),
				)
			}
		}
	}

	return u.k8sClient.Create(context.TODO(), ds)
}

func (u *uninstallPortworx) DeleteNodeWiper() error {
	ownerRef := metav1.NewControllerRef(u.cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeleteServiceAccount(u.k8sClient, pxNodeWiperServiceAccountName, u.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRole(u.k8sClient, pxNodeWiperClusterRoleName); err != nil {
		return err
	}
	if err := k8sutil.DeleteClusterRoleBinding(u.k8sClient, pxNodeWiperClusterRoleBindingName); err != nil {
		return err
	}
	if err := k8sutil.DeleteDaemonSet(u.k8sClient, pxNodeWiperDaemonSetName, u.cluster.Namespace, *ownerRef); err != nil {
		return err
	}
	return nil
}

func (u *uninstallPortworx) createServiceAccount(
	ownerRef *metav1.OwnerReference,
) error {
	return k8sutil.CreateOrUpdateServiceAccount(
		u.k8sClient,
		&v1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            pxNodeWiperServiceAccountName,
				Namespace:       u.cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
		},
		ownerRef,
	)
}

func (u *uninstallPortworx) createClusterRole() error {
	return k8sutil.CreateOrUpdateClusterRole(
		u.k8sClient,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: pxNodeWiperClusterRoleName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"security.openshift.io"},
					Resources:     []string{"securitycontextconstraints"},
					ResourceNames: []string{"privileged"},
					Verbs:         []string{"use"},
				},
				{
					APIGroups:     []string{"policy"},
					Resources:     []string{"podsecuritypolicies"},
					ResourceNames: []string{constants.PrivilegedPSPName},
					Verbs:         []string{"use"},
				},
			},
		},
	)
}

func (u *uninstallPortworx) createClusterRoleBinding() error {
	return k8sutil.CreateOrUpdateClusterRoleBinding(
		u.k8sClient,
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: pxNodeWiperClusterRoleBindingName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      pxNodeWiperServiceAccountName,
					Namespace: u.cluster.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     pxNodeWiperClusterRoleName,
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
	)
}

func getKVDBClient(endpoints []string, opts map[string]string) (kvdb.Kvdb, error) {
	var urlPrefix, kvdbType, kvdbName string
	for i, url := range endpoints {
		urlTokens := strings.Split(url, ":")
		if i == 0 {
			if urlTokens[0] == "etcd" {
				kvdbType = "etcd"
			} else if urlTokens[0] == "consul" {
				kvdbType = "consul"
			} else {
				return nil, fmt.Errorf("unknown discovery endpoint : %v in %v", urlTokens[0], endpoints)
			}
		}

		if urlTokens[1] == "http" {
			urlPrefix = "http"
			urlTokens[1] = ""
		} else if urlTokens[1] == "https" {
			urlPrefix = "https"
			urlTokens[1] = ""
		} else {
			urlPrefix = "http"
		}

		kvdbURL := ""
		for j, v := range urlTokens {
			if j == 0 {
				kvdbURL = urlPrefix
			} else {
				if v != "" {
					kvdbURL = kvdbURL + ":" + v
				}
			}
		}
		endpoints[i] = kvdbURL
	}

	var kvdbVersion string
	var err error
	for i, url := range endpoints {
		kvdbVersion, err = getKVDBVersion(kvdbType+"-kv", url, opts)
		if err == nil {
			break
		} else if i == len(endpoints)-1 {
			return nil, err
		}
	}

	switch kvdbVersion {
	case kvdb.ConsulVersion1:
		kvdbName = consul.Name
	case kvdb.EtcdBaseVersion:
		kvdbName = e2.Name
	case kvdb.EtcdVersion3:
		kvdbName = e3.Name
	default:
		return nil, fmt.Errorf("unknown kvdb endpoint (%v) and version (%v) ", endpoints, kvdbVersion)
	}

	return newKVDB(kvdbName, pxKvdbPrefix, endpoints, opts, nil)
}
