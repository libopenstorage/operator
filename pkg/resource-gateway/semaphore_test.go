package resource_gateway

import (
	"fmt"
	"os"
	"sync"
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
	// methods
	noaction  = "NoAction"
	acquire   = "Acquire"
	release   = "Release"
	heartbeat = "Heartbeat"

	// priorities
	nopriority = pb.AccessPriority_TYPE_UNSPECIFIED
	low        = pb.AccessPriority_LOW
	med        = pb.AccessPriority_MEDIUM
	high       = pb.AccessPriority_HIGH

	// access statuses
	nostatus = pb.AccessStatus_TYPE_UNSPECIFIED
	leased   = pb.AccessStatus_LEASED
	queued   = pb.AccessStatus_QUEUED
)

var (
	once sync.Once

	testConfigMapName         = "resource-gateway-test"
	testConfigMapNamespace    = "kube-system"
	testConfigMapUpdatePeriod = 1 * time.Second
	testLeaseTimeout          = 25 * time.Second
	testDeadClientTimeout     = 15 * time.Second
)

func newSemaphorePriorityQueueTest() SemaphorePriorityQueue {
	semaphoreConfig := &SemaphoreConfig{
		NPermits:           uint32(2),
		ConfigMapName:      testConfigMapName,
		ConfigMapNamespace: testConfigMapNamespace,
		ConfigMapLabels: map[string]string{
			"name": testConfigMapName,
		},
		ConfigMapUpdatePeriod: testConfigMapUpdatePeriod,
		LeaseTimeout:          testLeaseTimeout,
		DeadClientTimeout:     testDeadClientTimeout,
		MaxQueueSize:          1000,
	}
	return NewSemaphorePriorityQueueWithConfig(semaphoreConfig)
}

func deleteConfigMap(t *testing.T, suffix string) (err error) {
	defer require.NoError(t, err, "Unable to delete configmap")
	configMapName := testConfigMapName
	if suffix != "" {
		configMapName = fmt.Sprintf("%s-%s", testConfigMapName, suffix)
	}
	err = core.Instance().DeleteConfigMap(configMapName, testConfigMapNamespace)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return err
	}

	// wait for the configmap to be deleted and check every second upto 5s
	for i := 0; i < 5; i++ {
		_, err = core.Instance().GetConfigMap(configMapName, testConfigMapNamespace)
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		time.Sleep(time.Second * 1)
	}
	return err
}

func setup(t *testing.T) {
	// init
	once.Do(func() {
		if sched.Instance() == nil {
			sched.Init(time.Second)
		}

		// validate kubeconfig is set
		if os.Getenv("KUBECONFIG") == "" {
			fmt.Println("KUBECONFIG not set. Cannot run Semaphore UT.")
			return
		}
		logrus.SetLevel(logrus.DebugLevel)
		testOverride = true
	})

	deleteConfigMap(t, "") // cleanup
}

func validateConfigMap(t *testing.T, activeLeasesList []string) {
	remoteConfigMap, err := core.Instance().GetConfigMap(testConfigMapName, testConfigMapNamespace)
	cm := configMap{
		cm: remoteConfigMap,
	}
	require.NoError(t, err, "Unexpected error on get configmap")
	activeLeases, err := cm.ActiveLeases()
	require.NoError(t, err, "Unexpected error on fetch active leases from configmap")
	require.Equal(t, len(activeLeasesList), len(activeLeases))
	for _, expectedClient := range activeLeasesList {
		_, exists := activeLeases[expectedClient]
		require.True(t, exists, "Client %s not found in configmap", expectedClient)
	}
}

func TestSemaphore_AcquireAndRelease(t *testing.T) {

	testCases := []struct {
		name             string
		method           string
		delay            time.Duration
		client           string
		priority         pb.AccessPriority_Type
		status           pb.AccessStatus_Type
		activeLeasesList []string
	}{
		// configMapUpdatePeriod = 1s, leaseTimeout = 25s, deadClientTimeout = 15s
		// test acquire and release
		// tests a client can take the first lease
		{"client-1 is granted lease", acquire, 0, "client-1", low, leased, []string{"client-1"}},
		// 2 seconds elapsed
		// tests a client can take the second / last lease
		{"client-2 is granted lease", acquire, 0, "client-2", low, leased, []string{"client-1", "client-2"}},
		// 4 seconds elapsed
		// tests a client can be queued to low priority
		{"client-3 is queued to low", acquire, 0, "client-3", low, queued, []string{"client-1", "client-2"}},
		// tests a client can be queued to medium priority
		{"client-4 is queued to med", acquire, 0, "client-4", med, queued, []string{"client-1", "client-2"}},
		// tests a client can be queued to high priority
		{"client-5 is queued to high", acquire, 0, "client-5", high, queued, []string{"client-1", "client-2"}},
		// tests a client can be queued to high priority behind another high priority client
		{"client-6 is queued to high", acquire, 0, "client-6", high, queued, []string{"client-1", "client-2"}},
		// tests the acquire will be noop and return correct status if the client already has the lease
		{"client-1 is already leased", acquire, 0, "client-1", low, leased, []string{"client-1", "client-2"}},
		// tests the acquire will be noop if the client is already queued and no lease is available
		{"client-5 cannot take the lease", acquire, 0, "client-5", high, queued, []string{"client-1", "client-2"}},
		// tests client is able to release the lease
		{"client-1 releases the lease", release, 0, "client-1", nopriority, nostatus, []string{"client-2"}},
		// 6 seconds elapsed
		// tests that heartbeats return the correct status
		// heartbeat to keep client-2 lease alive till 21 seconds elapsed
		{"client-2 heartbeats within lease timeout", heartbeat, 0, "client-2", nopriority, leased, []string{"client-2"}},
		// do acquire poll for clients in queue for implicit heartbeat
		// keeps client 3 and 4 alive till 21 seconds elapsed
		// tests the client with lower priority will NOT get the lease if a client with higher priority is in the queue
		{"client-3 cannot take the lease", acquire, 0, "client-3", low, queued, []string{"client-2"}},
		{"client-4 cannot take the lease", acquire, 0, "client-4", med, queued, []string{"client-2"}},
		// tests the client with highest priority at the front of the queue will get the lease
		{"client-5 is granted the lease", acquire, 0, "client-5", high, leased, []string{"client-5", "client-2"}},
		// 8 seconds elapsed
		// keep client 6 alive till 23 seconds elapsed
		{"client-6 cannot take the lease", acquire, 0, "client-6", high, queued, []string{"client-2", "client-5"}},
		{"client-5 releases the lease", release, 0, "client-5", nopriority, nostatus, []string{"client-2"}},
		// 10 seconds elapsed
		{"client-6 is granted the lease", acquire, 0, "client-6", high, leased, []string{"client-2", "client-6"}},
		// 12 seconds elapsed
		// tests that heartbeats return the correct status
		{"client-2 heartbeats within lease timeout", heartbeat, 0, "client-2", nopriority, leased, []string{"client-2", "client-6"}},
		{"client-6 heartbeats", heartbeat, 0, "client-6", nopriority, leased, []string{"client-2", "client-6"}},
		{"client-6 releases the lease", release, 0, "client-6", nopriority, nostatus, []string{"client-2"}},
		// 14 seconds elapsed
		// tests the client with medium priority will get the lease
		{"client-4 is granted the lease", acquire, 0, "client-4", med, leased, []string{"client-2", "client-4"}},
		// 16 seconds elasped
		// tests that client in queue that has not polled within dead client timeout will be removed the queue
		// sleep 7 seconds
		{"client-3 heartbeats outside dead client timeout", heartbeat, time.Second * 7, "client-3", nopriority, nostatus, []string{"client-2", "client-4"}},
		// 23 seconds elapsed
		// tests that client that has held the lease for longer than the lease timeout will lose the lease
		// sleep 5 seconds
		{"client-2 heartbeats outside lease timeout", heartbeat, time.Second * 7, "client-2", nopriority, nostatus, []string{"client-4"}},
		// 30 seconds elapsed
		// tests that client that has a lease and has not heartbeated within dead client timeout will lose the lease
		// sleep 5 seconds
		{"client-4 heartbeats outside dead client timeout", heartbeat, time.Second * 5, "client-4", nopriority, nostatus, []string{}},
		// 35 seconds elapsed
	}

	setup(t)
	semPQ := newSemaphorePriorityQueueTest()

	startTime := time.Now()

	for _, tc := range testCases {
		if tc.delay > 0 {
			time.Sleep(tc.delay)
			fmt.Println("Elapsed time (after sleep): ", time.Since(startTime).Seconds())
		}
		if tc.method == acquire {
			t.Run(tc.name, func(t *testing.T) {
				status, err := semPQ.Acquire(tc.client, tc.priority)
				require.NoError(t, err, "Unexpected error on Acquire")
				require.Equal(t, tc.status, status)
			})
		} else if tc.method == release {
			t.Run(tc.name, func(t *testing.T) {
				err := semPQ.Release(tc.client)
				require.NoError(t, err, "Unexpected error on Release")
			})
		} else if tc.method == heartbeat {
			t.Run(tc.name, func(t *testing.T) {
				status := semPQ.Heartbeat(tc.client)
				require.Equal(t, tc.status, status)
			})
		}

		validateConfigMap(t, tc.activeLeasesList)

		fmt.Println("Elapsed time (after exec): ", time.Since(startTime).Seconds())
	}
}
