package server

import (
	"context"

	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	emptypb "google.golang.org/protobuf/types/known/emptypb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type server struct {
	k8s         core.Ops
	semaphorePQ SemaphorePriorityQueue
	pb.UnimplementedSemaphoreServiceServer
}

// TODO implement semaphore map for different resource ids
func NewServer() *server {
	s := &server{
		k8s:         core.Instance(),
		semaphorePQ: NewSemaphorePriorityQueue(),
	}
	return s
}

func (s *server) AcquireLock(ctx context.Context, req *pb.AcquireLockRequest) (*pb.AcquireLockResponse, error) {
	// validate request
	if req.ResourceId == "" {
		return &pb.AcquireLockResponse{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.ClientId == "" {
		return &pb.AcquireLockResponse{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}
	accessPriority := req.AccessPriority
	if accessPriority == pb.SemaphoreAccessPriority_TYPE_UNSPECIFIED {
		logrus.Debugf("Access priority not specified. Defaulting to LOW")
		accessPriority = pb.SemaphoreAccessPriority_LOW
	}

	// process request to acquire lock
	resourceState, err := s.semaphorePQ.AcquireLock(req.ClientId, accessPriority)
	if err != nil {
		return &pb.AcquireLockResponse{}, status.Error(codes.Internal, err.Error())
	}
	response := &pb.AcquireLockResponse{
		AccessStatus: resourceState,
	}
	return response, nil
}

func (s *server) ReleaseLock(ctx context.Context, req *pb.ReleaseLockRequest) (*emptypb.Empty, error) {
	// validate request
	if req.ResourceId == "" {
		return &emptypb.Empty{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.ClientId == "" {
		return &emptypb.Empty{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// process request to release lock
	err := s.semaphorePQ.ReleaseLock(req.ClientId)
	if err != nil {
		return &emptypb.Empty{}, status.Error(codes.Internal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *server) KeepAlive(ctx context.Context, req *pb.KeepAliveRequest) (*emptypb.Empty, error) {
	// validate request
	if req.ResourceId == "" {
		return &emptypb.Empty{}, status.Error(codes.InvalidArgument, "Resource ID is required")
	}
	if req.ClientId == "" {
		return &emptypb.Empty{}, status.Error(codes.InvalidArgument, "Client ID is required")
	}

	// process request to keep alive
	s.semaphorePQ.KeepAlive(req.ClientId)
	return &emptypb.Empty{}, nil
}
