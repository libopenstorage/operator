package component

import (
	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
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

	portworxProvisioner = "kubernetes.io/portworx-volume"
)

type portworxStorageClass struct {
	k8sClient client.Client
}

func (c *portworxStorageClass) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *portworxStorageClass) IsEnabled(cluster *corev1alpha1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster) && pxutil.StorageClassEnabled(cluster)
}

func (c *portworxStorageClass) Reconcile(cluster *corev1alpha1.StorageCluster) error {
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

	storageClasses := []*storagev1.StorageClass{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        PxDbStorageClass,
				Annotations: docAnnotations,
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: "db",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxDbEncryptedStorageClass,
				Annotations: map[string]string{
					"params/note": "Ensure that you have a cluster-wide secret created in the configured secrets provider",
				},
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel:   "3",
				api.SpecIoProfile: "db",
				api.SpecSecure:    "true",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxReplicatedStorageClass,
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: PxReplicatedEncryptedStorageClass,
			},
			Provisioner: portworxProvisioner,
			Parameters: map[string]string{
				api.SpecHaLevel: "2",
				api.SpecSecure:  "true",
			},
		},
	}

	if cluster.Spec.Stork != nil && cluster.Spec.Stork.Enabled {
		storageClasses = append(storageClasses,
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbLocalSnapshotStorageClass,
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbLocalSnapshotEncryptedStorageClass,
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					api.SpecSecure:  "true",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: local
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbCloudSnapshotStorageClass,
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
			},
			&storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: PxDbCloudSnapshotEncryptedStorageClass,
				},
				Provisioner: portworxProvisioner,
				Parameters: map[string]string{
					api.SpecHaLevel: "3",
					api.SpecSecure:  "true",
					"snapshotschedule.stork.libopenstorage.org/daily-schedule": `schedulePolicyName: default-daily-policy
annotations:
  portworx/snapshot-type: cloud
`,
				},
			},
		)
	}

	for _, sc := range storageClasses {
		if err := k8sutil.CreateStorageClass(c.k8sClient, sc); err != nil {
			return err
		}
	}
	return nil
}

func (c *portworxStorageClass) Delete(cluster *corev1alpha1.StorageCluster) error {
	if cluster.DeletionTimestamp != nil &&
		(cluster.Spec.DeleteStrategy == nil || cluster.Spec.DeleteStrategy.Type == "") {
		// If the cluster is deleted without any delete strategy do not delete the
		// storage classes as Portworx is still running on the nodes
		return nil
	}

	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxReplicatedStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbLocalSnapshotStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbCloudSnapshotStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbEncryptedStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxReplicatedEncryptedStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbLocalSnapshotEncryptedStorageClass); err != nil {
		return err
	}
	if err := k8sutil.DeleteStorageClass(c.k8sClient, PxDbCloudSnapshotEncryptedStorageClass); err != nil {
		return err
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
