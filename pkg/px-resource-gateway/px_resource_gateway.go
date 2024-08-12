package px_resource_gateway

import (
	"context"

	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type semaphoreServer struct {
	k8s         core.Ops
	semaphorePQ SemaphorePriorityQueue
}

// TODO implement semaphore map for different resource ids
func NewSemaphoreServer() *semaphoreServer {
	s := &semaphoreServer{
		k8s:         core.Instance(),
		semaphorePQ: NewSemaphorePriorityQueue(),
	}
	return s
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
	resourceState, err := s.semaphorePQ.AcquireLock(req.ClientId, req.AccessPriority)
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
	err := s.semaphorePQ.ReleaseLock(req.ClientId)
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
	s.semaphorePQ.KeepAlive(req.ClientId)
	return &pb.KeepAliveResponse{}, nil
}
