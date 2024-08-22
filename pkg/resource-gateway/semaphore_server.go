package resource_gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libopenstorage/operator/pkg/constants"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type semaphoreServer struct {
	semaphoreMap    sync.Map
	semaphoreConfig *SemaphoreConfig
}

// newSemaphoreServer creates a new semaphore server instance with the provided config.
func newSemaphoreServer(semaphoreConfig *SemaphoreConfig) *semaphoreServer {
	semaphoreConfig.ConfigMapLabels[constants.OperatorLabelManagedByKey] = constants.OperatorLabelManagedByValuePxResourceGateway
	return &semaphoreServer{
		semaphoreConfig: semaphoreConfig,
	}
}

func (s *semaphoreServer) createSemaphore(req *pb.CreateRequest) {
	// create a copy of the common semaphore config
	semaphoreConfig := copySemaphoreConfig(s.semaphoreConfig)

	// update the config with the required request parameters
	semaphoreConfig.NPermits = req.GetNPermits()
	semaphoreConfig.ConfigMapName = fmt.Sprintf("%s-%s", semaphoreConfig.ConfigMapName, req.GetResourceId())

	// override config values if optional request parameters are provided
	if req.GetLeaseTimeout() > 0 {
		semaphoreConfig.LeaseTimeout = time.Duration(req.GetLeaseTimeout()) * time.Second
	}
	if req.GetDeadNodeTimeout() > 0 {
		semaphoreConfig.DeadClientTimeout = time.Duration(req.GetDeadNodeTimeout()) * time.Second
	}

	logrus.Infof("Create semaphore with config: %v", semaphoreConfig)
	semaphore := CreateSemaphorePriorityQueue(semaphoreConfig)
	s.semaphoreMap.Store(req.GetResourceId(), semaphore)
}

// Create implements the Create RPC method of the SemaphoreService.
//
// Create is used to create a new semaphore instance and its backing configmap.
// It validates that required fields are provided in the request, and an instance
// with the same resource ID does not already exist.
func (s *semaphoreServer) Create(ctx context.Context, req *pb.CreateRequest) (*pb.CreateResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.CreateResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetNPermits() == 0 {
		return &pb.CreateResponse{}, status.Error(codes.InvalidArgument, "Number of leases should be greater than 0")
	}

	// check if semaphore instance already exists
	_, ok := s.semaphoreMap.Load(req.GetResourceId())
	if ok {
		return &pb.CreateResponse{}, status.Error(codes.AlreadyExists, "Resource already exists")
	}

	// process request to create resource
	s.createSemaphore(req)
	return &pb.CreateResponse{}, nil
}

// Load loads the semaphore instances that are already created in the system.
//
// It fetches all the configmaps with the label managed by px-resource-gateway
// and creates semaphore instances for each of them.
func (s *semaphoreServer) Load() error {
	configMapList, err := core.Instance().ListConfigMap(
		s.semaphoreConfig.ConfigMapName,
		metav1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s",
				constants.OperatorLabelManagedByKey, constants.OperatorLabelManagedByValuePxResourceGateway),
		})
	if err != nil {
		return err
	}
	for _, configMap := range configMapList.Items {
		// other config values will be populated later from the configMap data
		semaphoreConfig := &SemaphoreConfig{
			ConfigMapUpdatePeriod: s.semaphoreConfig.ConfigMapUpdatePeriod,
			DeadClientTimeout:     s.semaphoreConfig.DeadClientTimeout,
		}
		semaphore := NewSemaphorePriorityQueue(semaphoreConfig, &configMap)
		resourceId := configMap.Name[len(s.semaphoreConfig.ConfigMapName+"-"):]
		s.semaphoreMap.Store(resourceId, semaphore)
	}
	return nil
}

// Acquire implements the Acquire RPC method of the SemaphoreService.
//
// Acquire is used to acquire a lease on a resource. It validates that required
// fields are provided in the request, and the resource exists. It returns the
// status of the lease acquisition.
func (s *semaphoreServer) Acquire(ctx context.Context, req *pb.AcquireRequest) (*pb.AcquireResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.AcquireResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.AcquireResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}
	if req.GetAccessPriority() == pb.AccessPriority_TYPE_UNSPECIFIED {
		return &pb.AcquireResponse{}, status.Error(codes.InvalidArgument, "Access Priority is required")
	}

	// get the semaphore instance
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.AcquireResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	// process request to acquire lease
	resourceState, err := semaphorePQ.Acquire(req.ClientId, req.AccessPriority)
	if err != nil {
		return &pb.AcquireResponse{}, status.Error(codes.Internal, err.Error())
	}
	response := &pb.AcquireResponse{
		AccessStatus: resourceState,
	}
	return response, nil
}

// Release implements the Release RPC method of the SemaphoreService.
//
// Release is used to release a lease on a resource. It validates that required
// fields are provided in the request, and the resource exists.
// It returns an empty response.
func (s *semaphoreServer) Release(ctx context.Context, req *pb.ReleaseRequest) (*pb.ReleaseResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.ReleaseResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.ReleaseResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// get the semaphore instance
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.ReleaseResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	// process request to release lease
	err := semaphorePQ.Release(req.ClientId)
	if err != nil {
		return &pb.ReleaseResponse{}, status.Error(codes.Internal, err.Error())
	}
	return &pb.ReleaseResponse{}, nil
}

// Heartbeat implements the Heartbeat RPC method of the SemaphoreService.
//
// Heartbeat is used to keep the lease alive. It validates that required fields
// are provided in the request, and the resource exists. It returns the status
// of the lease.
func (s *semaphoreServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.HeartbeatResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.HeartbeatResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// get the semaphore instance
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.HeartbeatResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	// process client heartbeat
	accessStatus := semaphorePQ.Heartbeat(req.ClientId)
	response := &pb.HeartbeatResponse{
		AccessStatus: accessStatus,
	}
	return response, nil
}
