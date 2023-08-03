package component

import (
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxStorageClassComponentName name of the Portworx StorageClass component
	PortworxStorageClassComponentName = "Portworx StorageClass"
	// PxDbStorageClass name of the storage class for DB workloads
	PxDbStorageClass = "px-db"
	// PxReplicatedStorageClass name of the replicated storage class
	PxReplicatedStorageClass = "px-replicated"
	// PxDbLocalSnapshotStorageClass name of the storage class for local snapshots
	PxDbLocalSnapshotStorageClass = "px-db-local-snapshot"
	// PxDbCloudSnapshotStorageClass name of the storage class for cloud snapshots
	PxDbCloudSnapshotStorageClass = "px-db-cloud-snapshot"
	// PxDbEncryptedStorageClass name of the storage class for encrypted DB workloads
	PxDbEncryptedStorageClass = "px-db-encrypted"
	// PxReplicatedEncryptedStorageClass name of the replicated storage class with
	// encryption enabled
	PxReplicatedEncryptedStorageClass = "px-replicated-encrypted"
	// PxDbLocalSnapshotEncryptedStorageClass name of the storage class for local
	// snapshots with encryption enabled
	PxDbLocalSnapshotEncryptedStorageClass = "px-db-local-snapshot-encrypted"
	// PxDbCloudSnapshotEncryptedStorageClass name of the storage class for cloud
	// snapshots with encryption enabled
	PxDbCloudSnapshotEncryptedStorageClass = "px-db-cloud-snapshot-encrypted"

	// PxDbCSIStorageClass name of the CSI storage class for DB workloads
	PxDbCSIStorageClass = "px-csi-db"
	// PxReplicatedCSIStorageClass name of the replicated CSI storage class
	PxReplicatedCSIStorageClass = "px-csi-replicated"
	// PxDbLocalSnapshotCSIStorageClass name of the CSI storage class for local snapshots
	PxDbLocalSnapshotCSIStorageClass = "px-csi-db-local-snapshot"
	// PxDbCloudSnapshotCSIStorageClass name of the CSI storage class for cloud snapshots
	PxDbCloudSnapshotCSIStorageClass = "px-csi-db-cloud-snapshot"
	// PxDbEncryptedCSIStorageClass name of the CSI storage class for encrypted DB workloads
	PxDbEncryptedCSIStorageClass = "px-csi-db-encrypted"
	// PxReplicatedEncryptedCSIStorageClass name of the replicated CSI storage class with
	// encryption enabled
	PxReplicatedEncryptedCSIStorageClass = "px-csi-replicated-encrypted"
	// PxDbLocalSnapshotEncryptedCSIStorageClass name of the CSI storage class for local
	// snapshots with encryption enabled
	PxDbLocalSnapshotEncryptedCSIStorageClass = "px-csi-db-local-snapshot-encrypted"
	// PxDbCloudSnapshotEncryptedCSIStorageClass name of the CSI storage class for cloud
	// snapshots with encryption enabled
	PxDbCloudSnapshotEncryptedCSIStorageClass = "px-csi-db-cloud-snapshot-encrypted"

	PortworxInTreeProvisioner = "kubernetes.io/portworx-volume"
	PortworxCSIProvisioner    = "pxd.portworx.com"

	portworxIoProfile = "db_remote"
)

var (
	pxVer22, _ = version.NewVersion("2.2")
)

type portworxStorageClass struct {
	k8sClient  client.Client
	k8sVersion version.Version
}

func (c *portworxStorageClass) Name() string {
	return PortworxStorageClassComponentName
}

func (c *portworxStorageClass) Priority() int32 {
	return DefaultComponentPriority
}

func (c *portworxStorageClass) Initialize(
	k8sClient client.Client,
	k8sVersion version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
	c.k8sVersion = k8sVersion
}

func (c *portworxStorageClass) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *portworxStorageClass) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster) && pxutil.StorageClassEnabled(cluster)
}

func (c *portworxStorageClass) Reconcile(cluster *corev1.StorageCluster) error {
	docAnnotations := map[string]string{
		"params/docs":              "https://docs.portworx.com/scheduler/kubernetes/dynamic-provisioning.html",
		"params/fs":                "Filesystem to be laid out: none|xfs|ext4",
		"params/block_size":        "Block size",
		"params/repl":              "Replication factor for the volume: 1|2|3",
		"params/secure":            "Flag to create an encrypted volume: true|false",
		"params/shared":            "Flag to create a globally shared namespace volume which can be used by multiple pods: true|false",
		"params/priority_io":       "IO Priority: low|medium|high",
		"params/io_profile":        "IO Profile can be used to override the I/O algorithm Portworx uses for the volumes: db|sequential|random|cms",
		"params/aggregation_level": "Specifies the number of replication sets the volume can be aggregated from",
		"params/sticky":            "Flag to create sticky volumes that cannot be deleted until the flag is disabled",
		"params/journal":           "Flag to indicate if you want to use journal device for the volume's metadata. This will use the journal device that you used when installing Portworx. It is recommended to use a journal device to absorb PX metadata writes",
	}

	allowVolumeExpansion := true

	storageClasses := []*storagev1.StorageClass{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        PxDbStorageClass,
				Annotations: docAnnotations,
			},
			Provisioner: PortworxInTreeProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: portworxIoProfile,
			},
			AllowVolumeExpansion: &allowVolumeExpansion,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxDbEncryptedStorageClass,
				Annotations: map[string]string{
					"params/note": "Ensure that you have a cluster-wide secret created in the configured secrets provider",
				},
			},
			Provisioner: PortworxInTreeProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: portworxIoProfile,
				api.SpecSecure:    "true",
			},
			AllowVolumeExpansion: &allowVolumeExpansion,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxReplicatedStorageClass,
			},
			Provisioner: PortworxInTreeProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
			},
			AllowVolumeExpansion: &allowVolumeExpansion,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxReplicatedEncryptedStorageClass,
			},
			Provisioner: PortworxInTreeProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
				api.SpecSecure:  "true",
			},
			AllowVolumeExpansion: &allowVolumeExpansion,
		},
	}

	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		storageClasses = append(storageClasses,
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbLocalSnapshotStorageClass,
				},
				Provisioner: PortworxInTreeProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecIoProfile: portworxIoProfile,
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbLocalSnapshotEncryptedStorageClass,
				},
				Provisioner: PortworxInTreeProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecSecure:    "true",
					api.SpecIoProfile: portworxIoProfile,
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbCloudSnapshotStorageClass,
				},
				Provisioner: PortworxInTreeProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecIoProfile: portworxIoProfile,
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbCloudSnapshotEncryptedStorageClass,
				},
				Provisioner: PortworxInTreeProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecSecure:    "true",
					api.SpecIoProfile: portworxIoProfile,
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
		)
	}

	// Duplicate all in-tree SCs for CSI SCs for K8s 1.13+ and PX 2.2+ (Supported CSI Versions)
	var addCSIStorageClasses bool
	if c.k8sVersion.GreaterThanOrEqual(k8sutil.K8sVer1_13) && pxutil.GetPortworxVersion(cluster).GreaterThanOrEqual(pxVer22) {
		addCSIStorageClasses = true
	}

	if addCSIStorageClasses {
		csiStorageClasses := []*storagev1.StorageClass{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:        PxDbCSIStorageClass,
					Annotations: docAnnotations,
				},
				Provisioner: PortworxCSIProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecIoProfile: portworxIoProfile,
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbEncryptedCSIStorageClass,
					Annotations: map[string]string{
						"params/note": "Ensure that you have a cluster-wide secret created in the configured secrets provider",
					},
				},
				Provisioner: PortworxCSIProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel:   "3",
					api.SpecIoProfile: portworxIoProfile,
					api.SpecSecure:    "true",
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxReplicatedCSIStorageClass,
				},
				Provisioner: PortworxCSIProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "2",
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxReplicatedEncryptedCSIStorageClass,
				},
				Provisioner: PortworxCSIProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "2",
					api.SpecSecure:  "true",
				},
				AllowVolumeExpansion: &allowVolumeExpansion,
			},
		}

		if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
			csiStorageClasses = append(csiStorageClasses,
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: PxDbLocalSnapshotCSIStorageClass,
					},
					Provisioner: PortworxCSIProvisioner,
					Parameters: map[string]string{
						api.SpecHaLevel:   "3",
						api.SpecIoProfile: portworxIoProfile,
						"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
					},
					AllowVolumeExpansion: &allowVolumeExpansion,
				},
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: PxDbLocalSnapshotEncryptedCSIStorageClass,
					},
					Provisioner: PortworxCSIProvisioner,
					Parameters: map[string]string{
						api.SpecHaLevel:   "3",
						api.SpecSecure:    "true",
						api.SpecIoProfile: portworxIoProfile,
						"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
					},
					AllowVolumeExpansion: &allowVolumeExpansion,
				},
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: PxDbCloudSnapshotCSIStorageClass,
					},
					Provisioner: PortworxCSIProvisioner,
					Parameters: map[string]string{
						api.SpecHaLevel:   "3",
						api.SpecIoProfile: portworxIoProfile,
						"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
					},
					AllowVolumeExpansion: &allowVolumeExpansion,
				},
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: PxDbCloudSnapshotEncryptedCSIStorageClass,
					},
					Provisioner: PortworxCSIProvisioner,
					Parameters: map[string]string{
						api.SpecHaLevel:   "3",
						api.SpecSecure:    "true",
						api.SpecIoProfile: portworxIoProfile,
						"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
					},
					AllowVolumeExpansion: &allowVolumeExpansion,
				},
			)
		}

		storageClasses = append(storageClasses, csiStorageClasses...)
	}

	for _, sc := range storageClasses {
		if err := k8sutil.CreateStorageClass(c.k8sClient, sc); err != nil {
			return err
		}
	}
	return nil
}

func (c *portworxStorageClass) Delete(cluster *corev1.StorageCluster) error {
	if cluster.DeletionTimestamp != nil &&
		(cluster.Spec.DeleteStrategy == nil || cluster.Spec.DeleteStrategy.Type == "") {
		// If the cluster is deleted without any delete strategy do not delete the
		// storage classes as Portworx is still running on the nodes
		return nil
	}

	storageClassesToDelete := []string{
		PxDbStorageClass,
		PxReplicatedStorageClass,
		PxDbLocalSnapshotStorageClass,
		PxDbCloudSnapshotStorageClass,
		PxDbEncryptedStorageClass,
		PxReplicatedEncryptedStorageClass,
		PxDbLocalSnapshotEncryptedStorageClass,
		PxDbCloudSnapshotEncryptedStorageClass,
		PxDbCSIStorageClass,
		PxReplicatedCSIStorageClass,
		PxDbLocalSnapshotCSIStorageClass,
		PxDbCloudSnapshotCSIStorageClass,
		PxDbEncryptedCSIStorageClass,
		PxReplicatedEncryptedCSIStorageClass,
		PxDbLocalSnapshotEncryptedCSIStorageClass,
		PxDbCloudSnapshotEncryptedCSIStorageClass,
	}

	for _, scName := range storageClassesToDelete {
		if err := k8sutil.DeleteStorageClass(c.k8sClient, scName); err != nil {
			return err
		}
	}

	return nil
}

func (c *portworxStorageClass) MarkDeleted() {}

// RegisterPortworxStorageClassComponent registers the Portworx StorageClass component
func RegisterPortworxStorageClassComponent() {
	Register(PortworxStorageClassComponentName, &portworxStorageClass{})
}

func init() {
	RegisterPortworxStorageClassComponent()
}
