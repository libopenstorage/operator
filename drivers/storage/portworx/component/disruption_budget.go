package component

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// DisruptionBudgetComponentName name of the DisruptionBudget component
	DisruptionBudgetComponentName = "DisruptionBudget"
	// StoragePodDisruptionBudgetName name of the PodDisruptionBudget for portworx storage pods
	StoragePodDisruptionBudgetName = "px-storage"
	// KVDBPodDisruptionBudgetName name of the PodDisruptionBudget for portworx kvdb pods
	KVDBPodDisruptionBudgetName = "px-kvdb"
	// DefaultKVDBClusterSize is the default size of internal KVDB cluster
	DefaultKVDBClusterSize = 3
)

type disruptionBudget struct {
	k8sClient client.Client
	sdkConn   *grpc.ClientConn
}

func (c *disruptionBudget) Name() string {
	return DisruptionBudgetComponentName
}

func (c *disruptionBudget) Priority() int32 {
	return DefaultComponentPriority
}

func (c *disruptionBudget) Initialize(
	k8sClient client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	c.k8sClient = k8sClient
}

func (c *disruptionBudget) IsPausedForMigration(cluster *corev1.StorageCluster) bool {
	return util.ComponentsPausedForMigration(cluster)
}

func (c *disruptionBudget) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster) && pxutil.PodDisruptionBudgetEnabled(cluster)
}

func (c *disruptionBudget) Reconcile(cluster *corev1.StorageCluster) error {
	if pxutil.IsFreshInstall(cluster) {
		return nil
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createKVDBPodDisruptionBudget(cluster, ownerRef); err != nil {
		return err
	}
	// Create node PDB only if parallel upgrade is supported
	var err error
	c.sdkConn, err = pxutil.GetPortworxConn(c.sdkConn, c.k8sClient, cluster.Namespace)
	if err != nil {
		return err
	}

	// Get list of portworx storage nodes
	nodeClient := api.NewOpenStorageNodeClient(c.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, c.k8sClient)
	if err != nil {
		return err
	}
	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		ctx,
		&api.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		return fmt.Errorf("failed to enumerate nodes: %v", err)
	}

	if pxutil.ClusterSupportsParallelUpgrade(nodeEnumerateResponse) {
		if err := c.createPortworxNodePodDisruptionBudget(cluster, ownerRef, nodeEnumerateResponse); err != nil {
			return err
		}
	} else {
		if err := c.createPortworxPodDisruptionBudget(cluster, ownerRef, nodeEnumerateResponse); err != nil {
			return err
		}
	}

	return nil
}

func (c *disruptionBudget) Delete(cluster *corev1.StorageCluster) error {
	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := k8sutil.DeletePodDisruptionBudget(
		c.k8sClient, KVDBPodDisruptionBudgetName,
		cluster.Namespace, *ownerRef,
	); err != nil {
		return err
	}
	if err := k8sutil.DeletePodDisruptionBudget(
		c.k8sClient, StoragePodDisruptionBudgetName,
		cluster.Namespace, *ownerRef,
	); err != nil {
		return err
	}
	c.closeSdkConn()
	return nil
}

func (c *disruptionBudget) MarkDeleted() {}

func (c *disruptionBudget) createPortworxPodDisruptionBudget(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	nodeEnumerateResponse *api.SdkNodeEnumerateWithFiltersResponse,
) error {
	userProvidedMinValue, err := pxutil.MinAvailableForStoragePDB(cluster)
	if err != nil {
		logrus.Warnf("Invalid value for annotation %s: %v", pxutil.AnnotationStoragePodDisruptionBudget, err)
		userProvidedMinValue = -1
	}

	var minAvailable int

	storageNodesCount, err := pxutil.CountStorageNodes(cluster, c.sdkConn, c.k8sClient, nodeEnumerateResponse)
	if err != nil {
		c.closeSdkConn()
		return err
	}

	// Set minAvailable value to storagenodes-1 if no value is provided,
	// or if the user provided value is lesser storageNodes/2 +1 (px quorum)
	// or greater than or equal to the number of storage nodes.
	quorumValue := math.Floor(float64(storageNodesCount)/2) + 1
	if userProvidedMinValue < int(quorumValue) || userProvidedMinValue >= storageNodesCount {
		logrus.Warnf("Value for px-storage pod disruption budget not provided or is invalid, using default calculated value %d: ", storageNodesCount-1)
		minAvailable = storageNodesCount - 1
	} else {
		minAvailable = userProvidedMinValue
	}

	// Create PDB only if there are at least 3 nodes. With 2 nodes or less, if 1
	// node goes down Portworx will lose quorum anyway. Such clusters would be
	// non-prod clusters and there is no point in blocking the evictions.
	if minAvailable > 1 {
		minAvailableIntStr := intstr.FromInt(minAvailable)
		pdb := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:            StoragePodDisruptionBudgetName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailableIntStr,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						constants.LabelKeyClusterName: cluster.Name,
						constants.LabelKeyStoragePod:  constants.LabelValueTrue,
					},
				},
			},
		}

		err = k8sutil.CreateOrUpdatePodDisruptionBudget(c.k8sClient, pdb, ownerRef)
	}
	return err
}

func (c *disruptionBudget) createPortworxNodePodDisruptionBudget(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
	nodeEnumerateResponse *api.SdkNodeEnumerateWithFiltersResponse,
) error {
	nodesNeedingPDB, err := pxutil.NodesNeedingPDB(c.k8sClient, nodeEnumerateResponse)
	if err != nil {
		return err
	}
	for _, node := range nodesNeedingPDB {
		minAvailable := intstr.FromInt(1)
		PDBName := "px-" + node
		pdb := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:            PDBName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						constants.LabelKeyClusterName:      cluster.Name,
						constants.OperatorLabelNodeNameKey: node,
					},
				},
			},
		}
		err = k8sutil.CreateOrUpdatePodDisruptionBudget(c.k8sClient, pdb, ownerRef)
		if err != nil {
			logrus.Warnf("Failed to create PDB for node %s: %v", node, err)
			break
		}
	}
	return err

}

func (c *disruptionBudget) createKVDBPodDisruptionBudget(
	cluster *corev1.StorageCluster,
	ownerRef *metav1.OwnerReference,
) error {
	// No need to create PDB for KVDB when using external KVDB
	if cluster.Spec.Kvdb != nil && !cluster.Spec.Kvdb.Internal {
		return nil
	}
	clusterSize := kvdbClusterSize(cluster)
	minAvailable := intstr.FromInt(clusterSize - 1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:            KVDBPodDisruptionBudgetName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.LabelKeyClusterName: cluster.Name,
					constants.LabelKeyKVDBPod:     constants.LabelValueTrue,
				},
			},
		},
	}
	return k8sutil.CreateOrUpdatePodDisruptionBudget(c.k8sClient, pdb, ownerRef)
}

// closeSdkConn closes the sdk connection and resets it to nil
func (c *disruptionBudget) closeSdkConn() {
	if c.sdkConn == nil {
		return
	}

	if err := c.sdkConn.Close(); err != nil {
		logrus.Errorf("Failed to close sdk connection: %s", err.Error())
	}
	c.sdkConn = nil
}

func kvdbClusterSize(cluster *corev1.StorageCluster) int {
	args, err := pxutil.MiscArgs(cluster)
	if err != nil {
		logrus.Warnf("error parsing misc args: %v", err)
		return DefaultKVDBClusterSize
	}

	if len(args) == 0 {
		return DefaultKVDBClusterSize
	}

	argName := "-kvdb_cluster_size"
	for args[len(args)-1] == argName {
		args = args[:len(args)-1]
	}

	var kvdbClusterSizeStr string
	for i, arg := range args {
		if arg == argName {
			kvdbClusterSizeStr = args[i+1]
			break
		}
	}

	if len(kvdbClusterSizeStr) > 0 {
		size, err := strconv.Atoi(kvdbClusterSizeStr)
		if err == nil {
			return size
		}
		logrus.Warnf("Invalid value %v for -kvdb_cluster_size in misc args. %v", size, err)
	}
	return DefaultKVDBClusterSize
}

// RegisterDisruptionBudgetComponent registers the Portworx DisruptionBudget component
func RegisterDisruptionBudgetComponent() {
	Register(DisruptionBudgetComponentName, &disruptionBudget{})
}

func init() {
	RegisterDisruptionBudgetComponent()
}
