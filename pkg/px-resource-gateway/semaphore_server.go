package px_resource_gateway

import (
	"context"
	"sync"
	"time"

	pb "github.com/libopenstorage/operator/proto"
	"github.com/sirupsen/logrus"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var semaphoreConfigCommon *SemaphoreConfig

type semaphoreServer struct {
	semaphoreMap sync.Map
}

func NewSemaphoreServer(semaphoreConfig *SemaphoreConfig) *semaphoreServer {
	semaphoreConfigCommon = semaphoreConfig
	return &semaphoreServer{}
}

func (s *semaphoreServer) CreateResource(ctx context.Context, req *pb.CreateResourceRequest) (*pb.CreateResourceResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.CreateResourceResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetNLocks() == 0 {
		return &pb.CreateResourceResponse{}, status.Error(codes.InvalidArgument, "Number of locks should be greater than 0")
	}
	if req.GetLockHoldTimeout() == 0 {
		return &pb.CreateResourceResponse{}, status.Error(codes.InvalidArgument, "Lock hold timeout should be greater than 0")
	}

	// process request to create resource
	_, ok := s.semaphoreMap.Load(req.GetResourceId())
	if ok {
		return &pb.CreateResourceResponse{}, status.Error(codes.AlreadyExists, "Resource already exists")
	}

	// TODO: add lock hold timeout to semaphore
	configMapLabels := make(map[string]string)
	for k, v := range semaphoreConfigCommon.ConfigMapLabels {
		configMapLabels[k] = v
	}
	semaphoreConfig := &SemaphoreConfig{
		NLocks:                req.GetNLocks(),
		ConfigMapName:         semaphoreConfigCommon.ConfigMapName + "-" + req.GetResourceId(),
		ConfigMapNamespace:    semaphoreConfigCommon.ConfigMapNamespace,
		ConfigMapLabels:       configMapLabels,
		ConfigMapUpdatePeriod: semaphoreConfigCommon.ConfigMapUpdatePeriod,
		DeadNodeTimeout:       semaphoreConfigCommon.DeadNodeTimeout,
		LockHoldTimeout:       time.Duration(req.GetLockHoldTimeout()),
	}
	logrus.Infof("Creating semaphore with config: %v", semaphoreConfig)

	semaphore := NewSemaphorePriorityQueue(semaphoreConfig)
	s.semaphoreMap.Store(req.GetResourceId(), semaphore)

	return &pb.CreateResourceResponse{}, nil
}

func (s *semaphoreServer) AcquireLock(ctx context.Context, req *pb.AcquireLockRequest) (*pb.AcquireLockResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.AcquireLockResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.AcquireLockResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}
	if req.GetAccessPriority() == pb.SemaphoreAccessPriority_TYPE_UNSPECIFIED {
		return &pb.AcquireLockResponse{}, status.Error(codes.InvalidArgument, "Access Priority is required")
	}

	// process request to acquire lock
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.AcquireLockResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	resourceState, err := semaphorePQ.AcquireLock(req.ClientId, req.AccessPriority)
	if err != nil {
		return &pb.AcquireLockResponse{}, status.Error(codes.Internal, err.Error())
	}
	response := &pb.AcquireLockResponse{
		AccessStatus: resourceState,
	}
	return response, nil
}

func (s *semaphoreServer) ReleaseLock(ctx context.Context, req *pb.ReleaseLockRequest) (*pb.ReleaseLockResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.ReleaseLockResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.ReleaseLockResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// process request to release lock
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.ReleaseLockResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	err := semaphorePQ.ReleaseLock(req.ClientId)
	if err != nil {
		return &pb.ReleaseLockResponse{}, status.Error(codes.Internal, err.Error())
	}
	return &pb.ReleaseLockResponse{}, nil
}

func (s *semaphoreServer) KeepAlive(ctx context.Context, req *pb.KeepAliveRequest) (*pb.KeepAliveResponse, error) {
	// validate request
	if req.GetResourceId() == "" {
		return &pb.KeepAliveResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.GetClientId() == "" {
		return &pb.KeepAliveResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// process request to keep alive
	item, ok := s.semaphoreMap.Load(req.GetResourceId())
	if !ok {
		return &pb.KeepAliveResponse{}, status.Error(codes.NotFound, "Resource not found")
	}
	semaphorePQ := item.(SemaphorePriorityQueue)

	semaphorePQ.KeepAlive(req.ClientId)
	return &pb.KeepAliveResponse{}, nil
}
