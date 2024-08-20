package px_resource_gateway

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

var semaphoreConfigCommon *SemaphoreConfig

type semaphoreServer struct {
	semaphoreMap sync.Map
}

func NewSemaphoreServer(semaphoreConfig *SemaphoreConfig) *semaphoreServer {
	semaphoreConfigCommon = semaphoreConfig
	semaphoreConfigCommon.ConfigMapLabels[constants.OperatorLabelManagedByKey] = constants.OperatorLabelManagedByValuePxResourceGateway
	return &semaphoreServer{}
}

func (s *semaphoreServer) createResource(req *pb.CreateResourceRequest) {
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
		LockHoldTimeout:       time.Duration(req.GetLockHoldTimeout()) * time.Second,
		MaxQueueSize:          semaphoreConfigCommon.MaxQueueSize,
	}
	logrus.Infof("Creating semaphore with config: %v", semaphoreConfig)

	semaphore := CreateSemaphorePriorityQueue(semaphoreConfig)
	s.semaphoreMap.Store(req.GetResourceId(), semaphore)
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

	s.createResource(req)
	return &pb.CreateResourceResponse{}, nil
}

func (s *semaphoreServer) LoadResources() error {
	configMapList, err := core.Instance().ListConfigMap(
		semaphoreConfigCommon.ConfigMapNamespace,
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
			ConfigMapUpdatePeriod: semaphoreConfigCommon.ConfigMapUpdatePeriod,
			DeadNodeTimeout:       semaphoreConfigCommon.DeadNodeTimeout,
		}
		semaphore := NewSemaphorePriorityQueue(semaphoreConfig, &configMap)
		resourceId := configMap.Name[len(semaphoreConfigCommon.ConfigMapName+"-"):]
		s.semaphoreMap.Store(resourceId, semaphore)
	}
	return nil
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

	accessStatus := semaphorePQ.KeepAlive(req.ClientId)
	response := &pb.KeepAliveResponse{
		AccessStatus: accessStatus,
	}
	return response, nil
}
