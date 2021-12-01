package component

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/openstorage/api"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/util"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	if cluster.Status.Phase == "" || cluster.Status.Phase == string(corev1.ClusterInit) {
		return nil
	}

	ownerRef := metav1.NewControllerRef(cluster, pxutil.StorageClusterKind())
	if err := c.createKVDBPodDisruptionBudget(cluster, ownerRef); err != nil {
		return err
	}
	if err := c.createPortworxPodDisruptionBudget(cluster, ownerRef); err != nil {
		return err
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
) error {
	var err error
	c.sdkConn, err = pxutil.GetPortworxConn(c.sdkConn, c.k8sClient, cluster.Namespace)
	if err != nil {
		return err
	}

	nodeClient := api.NewOpenStorageNodeClient(c.sdkConn)
	ctx, err := pxutil.SetupContextWithToken(context.Background(), cluster, c.k8sClient)
	if err != nil {
		c.closeSdkConn()
		return err
	}

	nodeEnumerateResponse, err := nodeClient.EnumerateWithFilters(
		ctx,
		&api.SdkNodeEnumerateWithFiltersRequest{},
	)
	if err != nil {
		c.closeSdkConn()
		return fmt.Errorf("failed to enumerate nodes: %v", err)
	}

	storageNodesCount := 0
	for _, node := range nodeEnumerateResponse.Nodes {
		if len(node.Pools) > 0 && node.Pools[0] != nil {
			storageNodesCount++
		}
	}

	// Create PDB only if there are at least 3 nodes. With 2 nodes are less, if 1
	// node goes down Portworx will lose quorum anyway. Such clusters would be
	// non-prod clusters and there is no point in blocking the evictions.
	if storageNodesCount > 2 {
		minAvailable := intstr.FromInt(storageNodesCount - 1)
		pdb := &policyv1beta1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:            StoragePodDisruptionBudgetName,
				Namespace:       cluster.Namespace,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: policyv1beta1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						constants.LabelKeyClusterName: cluster.Name,
						constants.LabelKeyStoragePod:  constants.LabelValueTrue,
					},
				},
			},
		}
		return k8sutil.CreateOrUpdatePodDisruptionBudget(c.k8sClient, pdb, ownerRef)
	}
	return nil
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
	pdb := &policyv1beta1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:            KVDBPodDisruptionBudgetName,
			Namespace:       cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{*ownerRef},
		},
		Spec: policyv1beta1.PodDisruptionBudgetSpec{
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
