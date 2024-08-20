package px_resource_gateway

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/libopenstorage/openstorage/pkg/sched"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	testConfigMapName         = "px-resource-gateway-test"
	testConfigMapNamespace    = "kube-system"
	testConfigMapUpdatePeriod = 1 * time.Second
	testDeadNodeTimeout       = 5 * time.Second
	testLockHoldTimeout       = 20 * time.Second
)

func newSemaphorePriorityQueueTest() *semaphorePriorityQueue {
	semaphoreConfig := &SemaphoreConfig{
		NLocks:             uint32(2),
		ConfigMapName:      testConfigMapName,
		ConfigMapNamespace: testConfigMapNamespace,
		ConfigMapLabels: map[string]string{
			"name": testConfigMapName,
		},
		ConfigMapUpdatePeriod: testConfigMapUpdatePeriod,
		DeadNodeTimeout:       testDeadNodeTimeout,
		LockHoldTimeout:       testLockHoldTimeout,
		MaxQueueSize:          1000,
	}
	return createSemaphorePriorityQueue(semaphoreConfig)
}

func deleteConfigMap(t *testing.T) (err error) {
	defer require.NoError(t, err, "Unable to delete configmap")

	err = core.Instance().DeleteConfigMap(testConfigMapName, testConfigMapNamespace)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return err
	}

	// wait for the configmap to be deleted and check every second upto 5s
	for i := 0; i < 5; i++ {
		_, err = core.Instance().GetConfigMap(testConfigMapName, testConfigMapNamespace)
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		time.Sleep(time.Second * 1)
	}
	return err
}

func TestSemaphore(t *testing.T) {
	// init
	sched.Init(time.Second)

	// validate kubeconfig is set
	if os.Getenv("KUBECONFIG") == "" {
		fmt.Println("KUBECONFIG not set. Cannot run Semaphore UT.")
		return
	}
	logrus.SetLevel(logrus.DebugLevel)

	tests := map[string]func(*testing.T){
		"testAcquireAndRelease": testAcquireAndRelease,
	}
	for tName, tFunc := range tests {
		deleteConfigMap(t) // cleanup
		t.Run(tName, tFunc)
	}
}

func testAcquireAndRelease(t *testing.T) {

	const (
		// methods
		acquire = "AcquireLock"
		release = "ReleaseLock"

		// priorities
		nopriority = pb.SemaphoreAccessPriority_TYPE_UNSPECIFIED
		low        = pb.SemaphoreAccessPriority_LOW
		med        = pb.SemaphoreAccessPriority_MEDIUM
		high       = pb.SemaphoreAccessPriority_HIGH

		// access statuses
		nostatus = pb.SemaphoreAccessStatus_TYPE_UNSPECIFIED
		locked   = pb.SemaphoreAccessStatus_LOCKED
		queued   = pb.SemaphoreAccessStatus_QUEUED
	)

	// getCM (getConfigMap) returns map of clients and their lock ids
	// to validate the configmap values for each test case
	getCM := func(clients ...string) map[string]uint32 {
		cm := make(map[string]uint32)
		for i, client := range clients {
			if client == "" {
				continue
			}
			cm[client] = uint32(i + 1)
		}
		return cm
	}

	testCases := []struct {
		name      string
		method    string
		client    string
		priority  pb.SemaphoreAccessPriority_Type
		status    pb.SemaphoreAccessStatus_Type
		configMap map[string]uint32
	}{
		{"node-1 is granted lock", acquire, "node-1", low, locked, getCM("node-1", "")},             // tests a client can take the first lock
		{"node-2 is granted lock", acquire, "node-2", low, locked, getCM("node-1", "node-2")},       // tests a client can simultaneously take the second / last lock
		{"node-3 is queued to low", acquire, "node-3", low, queued, getCM("node-1", "node-2")},      // tests a client can be queued to low priority
		{"node-4 is queued to med", acquire, "node-4", med, queued, getCM("node-1", "node-2")},      // tests a client can be queued to medium priority
		{"node-5 is queued to high", acquire, "node-5", high, queued, getCM("node-1", "node-2")},    // tests a client can be queued to high priority
		{"node-6 is queued to high", acquire, "node-6", high, queued, getCM("node-1", "node-2")},    // tests a client can be queued to high priority behind another high priority client
		{"node-1 is already locked", acquire, "node-1", low, locked, getCM("node-1", "node-2")},     // tests the request will be noop if the client already has the lock
		{"node-5 cannot take the lock", acquire, "node-5", high, queued, getCM("node-1", "node-2")}, // tests the request will be noop if the client is already queued and no lock is available
		{"node-1 releases the lock", release, "node-1", 0, 0, getCM("", "node-2")},                  // tests client is able to release the lock
		{"node-3 cannot take the lock", acquire, "node-3", low, queued, getCM("", "node-2")},        // tests the client with lower priority will NOT get the lock if a client with higher priority is in the queue
		{"node-5 is granted the lock", acquire, "node-5", high, locked, getCM("node-5", "node-2")},  // tests the client with highest priority at the front of the queue will get the lock
		{"node-2 releases the lock", release, "node-2", 0, 0, getCM("node-5")},                      // tests the client is able to release the last lock
		{"node-6 is granted the lock", acquire, "node-6", high, locked, getCM("node-5", "node-6")},  //
		{"node-5 releases the lock", release, "node-5", 0, 0, getCM("", "node-6")},                  //
		{"node-6 releases the lock", release, "node-6", 0, 0, getCM("", "")},                        // tests the client is able to release the last lock
		{"node-4 is granted the lock", acquire, "node-4", med, locked, getCM("node-4")},             // tests the client with medium priority will get the lock
		{"node-3 is granted the lock", acquire, "node-3", low, locked, getCM("node-4", "node-3")},   // tests the last client (with low priority) will get the lock
	}

	semPQ := newSemaphorePriorityQueueTest()

	for _, tc := range testCases {
		if tc.method == acquire {
			t.Run(tc.name, func(t *testing.T) {
				status, err := semPQ.AcquireLock(tc.client, tc.priority)
				require.NoError(t, err, "Unexpected error on AcquireLock")
				require.Equal(t, tc.status, status)
			})
		} else if tc.method == release {
			t.Run(tc.name, func(t *testing.T) {
				err := semPQ.ReleaseLock(tc.client)
				require.NoError(t, err, "Unexpected error on ReleaseLock")
			})
		}

		// validate configmap
		cm, err := core.Instance().GetConfigMap(testConfigMapName, testConfigMapNamespace)
		require.NoError(t, err, "Unexpected error on GetConfigMap")
		locks, err := getLocksFromConfigMap(cm)
		require.NoError(t, err, "Unexpected error on getLocksFromConfigMap")
		require.Equal(t, len(tc.configMap), len(locks))
		for client, expectedLock := range tc.configMap {
			lockObj, exists := locks[client]
			require.True(t, exists, "Client %s not found in configmap", client)
			require.Equal(t, expectedLock, lockObj.Id)
		}
	}
}

func testKeepAlive(t *testing.T) {
	semPQ := newSemaphorePriorityQueueTest()

	// acquire lock
	status, err := semPQ.AcquireLock("node-1", pb.SemaphoreAccessPriority_LOW)
	require.NoError(t, err, "Unexpected error on AcquireLock")
	require.Equal(t, pb.SemaphoreAccessStatus_LOCKED, status)

	// keep alive
	time.Sleep(testDeadNodeTimeout / 2)
	semPQ.KeepAlive("node-1")
	require.NoError(t, err, "Unexpected error on KeepAlive")

	time.Sleep(testDeadNodeTimeout)
	semPQ.KeepAlive("node-1")
	require.NoError(t, err, "Unexpected error on KeepAlive")

	// validate configmap
	cm, err := core.Instance().GetConfigMap(testConfigMapName, testConfigMapNamespace)
	require.NoError(t, err, "Unexpected error on GetConfigMap")
	logrus.Infof("ConfigMap: %v", cm)
	require.Equal(t, 1, len(cm.Data))
	require.Equal(t, "1", cm.Data["node-1"])
}
