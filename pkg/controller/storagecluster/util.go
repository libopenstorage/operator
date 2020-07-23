package storagecluster

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"reflect"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	hashutil "k8s.io/kubernetes/pkg/util/hash"
)

// computeHash returns a hash value calculated from StorageClusterSpec and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
func computeHash(clusterSpec *corev1.StorageClusterSpec, collisionCount *int32) string {
	storageClusterSpecHasher := fnv.New32a()
	hashutil.DeepHashObject(storageClusterSpecHasher, *clusterSpec)

	// Add collisionCount in the hash if it exists.
	if collisionCount != nil {
		collisionCountBytes := make([]byte, 8)
		binary.LittleEndian.PutUint32(collisionCountBytes, uint32(*collisionCount))
		storageClusterSpecHasher.Write(collisionCountBytes)
	}

	return rand.SafeEncodeString(fmt.Sprint(storageClusterSpecHasher.Sum32()))
}

func indexByPodNodeName(obj runtime.Object) []string {
	pod, isPod := obj.(*v1.Pod)
	if !isPod {
		return []string{}
	}
	// We are only interested in active pods with nodeName set
	if len(pod.Spec.NodeName) == 0 ||
		pod.Status.Phase == v1.PodSucceeded ||
		pod.Status.Phase == v1.PodFailed {
		return []string{}
	}
	return []string{pod.Spec.NodeName}
}

func historyName(clusterName, hash string) string {
	return clusterName + "-" + hash
}

func deleteFinalizerExists(cluster *corev1.StorageCluster) bool {
	for _, finalizerName := range cluster.Finalizers {
		if finalizerName == deleteFinalizerName {
			return true
		}
	}
	return false
}

func removeDeleteFinalizer(finalizers []string) []string {
	newFinalizers := []string{}
	for _, finalizer := range finalizers {
		if finalizer == deleteFinalizerName {
			continue
		}
		newFinalizers = append(newFinalizers, finalizer)
	}
	return newFinalizers
}

func isEnvEqual(listA, listB []v1.EnvVar) bool {
	if len(listA) != len(listB) {
		return false
	}

	aLen := len(listA)
	bLen := len(listB)
	visited := make([]bool, aLen)

	for i := 0; i < aLen; i++ {
		found := false
		for j := 0; j < bLen; j++ {
			if visited[j] {
				continue
			}
			if reflect.DeepEqual(listA[i], listB[j]) {
				visited[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
