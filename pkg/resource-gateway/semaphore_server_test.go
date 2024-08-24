package resource_gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/libopenstorage/operator/pkg/constants"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newSemaphoreServerTest() *semaphoreServer {
	semaphoreConfig := &SemaphoreConfig{
		ConfigMapName:         testConfigMapName,
		ConfigMapNamespace:    testConfigMapNamespace,
		ConfigMapLabels:       map[string]string{},
		ConfigMapUpdatePeriod: testConfigMapUpdatePeriod,
		LeaseTimeout:          testLeaseTimeout,
		DeadClientTimeout:     testDeadClientTimeout,
		MaxQueueSize:          1000,
	}
	return NewSemaphoreServer(semaphoreConfig)
}

func createTestConfigMap(t *testing.T, suffix string) {
	configMapName := fmt.Sprintf("%s-%s", testConfigMapName, suffix)
	_, err := core.Instance().CreateConfigMap(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: testConfigMapNamespace,
			Labels: map[string]string{
				constants.OperatorLabelManagedByKey: constants.OperatorLabelManagedByValueResourceGateway,
			},
		},
		Data: map[string]string{
			activeLeasesKey:          "",
			nPermitsKey:              "1",
			configMapUpdatePeriodKey: testConfigMapUpdatePeriod.String(),
			leaseTimeoutKey:          testLeaseTimeout.String(),
			deadClientTimeoutKey:     testDeadClientTimeout.String(),
			maxQueueSizeKey:          "1000",
		},
	})
	require.NoError(t, err)
}

func TestSemaphoreServer_Create(t *testing.T) {
	setup(t)

	testResourceId := "resource-x"
	deleteConfigMap(t, testResourceId)
	s := newSemaphoreServerTest()

	// First create request for a semaphore will create a new configmap
	configMapName := fmt.Sprintf("%s-%s", testConfigMapName, testResourceId)
	req := &pb.CreateRequest{
		ResourceId:   testResourceId,
		NPermits:     1,
		LeaseTimeout: 10,
	}
	_, err := s.Create(context.Background(), req)
	require.NoError(t, err)
	remoteConfigMap, err := core.Instance().GetConfigMap(configMapName, testConfigMapNamespace)
	require.NoError(t, err)
	require.Equal(t, "1", remoteConfigMap.Data[nPermitsKey])

	// Second request for the same semaphore will update the existing configmap
	req = &pb.CreateRequest{
		ResourceId:   testResourceId,
		NPermits:     2,
		LeaseTimeout: 10,
	}
	_, err = s.Create(context.Background(), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Resource already exists")
	remoteConfigMap, err = core.Instance().GetConfigMap(configMapName, testConfigMapNamespace)
	require.NoError(t, err)
	require.Equal(t, "2", remoteConfigMap.Data[nPermitsKey])
}

func TestSemaphoreServer_Load(t *testing.T) {
	setup(t)

	s := newSemaphoreServerTest()
	deleteConfigMap(t, "resource-1")
	deleteConfigMap(t, "resource-2")

	// Create configmaps for semaphore that should be loaded
	createTestConfigMap(t, "resource-1")
	createTestConfigMap(t, "resource-2")

	// Load the semaphore
	err := s.Load()
	require.NoError(t, err)

	req := &pb.AcquireRequest{
		ResourceId:     "resource-1",
		ClientId:       "client-1",
		AccessPriority: pb.AccessPriority_HIGH,
	}
	resp, err := s.Acquire(context.Background(), req)
	require.NoError(t, err, "Unexpected error on Acquire")
	require.Equal(t, pb.AccessStatus_LEASED, resp.GetAccessStatus())

	req = &pb.AcquireRequest{
		ResourceId:     "resource-2",
		ClientId:       "client-2",
		AccessPriority: pb.AccessPriority_LOW,
	}
	resp, err = s.Acquire(context.Background(), req)
	require.NoError(t, err, "Unexpected error on Acquire")
	require.Equal(t, pb.AccessStatus_LEASED, resp.GetAccessStatus())
}
