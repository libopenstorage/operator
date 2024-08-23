package resource_gateway

import (
	"fmt"
	"os"
	"time"

	"github.com/libopenstorage/grpc-framework/pkg/util"
	grpcFramework "github.com/libopenstorage/grpc-framework/server"
	"github.com/libopenstorage/openstorage/pkg/sched"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	// defaultServerName is the default server name for resource-gateway
	defaultServerName = "resource-gateway"
	// defaultServerHost is the default server host for resource-gateway
	defaultServerHost = "127.0.0.1"
	// defaultServerPort is the default server port for resource-gateway
	defaultServerPort = "50051"
)

// ResourceGatewayServer is the main struct for resource-gateway gRPC server
//
// It contains the gRPC server instance and various component servers like semaphore and health check
type ResourceGatewayServer struct {
	server *grpcFramework.Server

	semaphoreServer   *semaphoreServer
	healthCheckServer *health.Server
}

// NewResourceGatewayServer creates a new resource-gateway gRPC server instance
func NewResourceGatewayServer(
	resourceGatewayConfig *grpcFramework.ServerConfig,
	semaphoreConfig *SemaphoreConfig,
) *ResourceGatewayServer {
	// create component servers
	healthCheckServer := health.NewServer()
	semaphoreServer := NewSemaphoreServer(semaphoreConfig)

	// register the component servers with the gRPC server config
	resourceGatewayConfig.
		RegisterGrpcServers(func(gs *grpc.Server) {
			pb.RegisterSemaphoreServiceServer(gs, semaphoreServer)
		}).
		RegisterGrpcServers(func(gs *grpc.Server) {
			healthpb.RegisterHealthServer(gs, healthCheckServer)
		}).
		WithDefaultGenericRoleManager()

	// create the gRPC server instance
	resourceGatewayGRPCServer, err := grpcFramework.New(resourceGatewayConfig)
	if err != nil {
		fmt.Printf("Unable to create server: %v", err)
		os.Exit(1)
	}

	resourceGatewayServer := &ResourceGatewayServer{
		server:            resourceGatewayGRPCServer,
		semaphoreServer:   semaphoreServer,
		healthCheckServer: healthCheckServer,
	}
	return resourceGatewayServer
}

func NewResourceGatewayServerConfig() *grpcFramework.ServerConfig {
	defaultServerAddress := fmt.Sprintf("%s:%s", defaultServerHost, defaultServerPort)
	resourceGatewayServerConfig := &grpcFramework.ServerConfig{
		Name:         defaultServerHost,
		Address:      defaultServerAddress,
		AuditOutput:  os.Stdout,
		AccessOutput: os.Stdout,
	}
	return resourceGatewayServerConfig
}

// SetupSigIntHandler sets up a signal handler to stop the server
func (r *ResourceGatewayServer) SetupSigIntHandler() error {
	signal_handler := util.NewSigIntManager(func() {
		r.server.Stop()
		r.healthCheckServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		r.server.Address()
		os.Exit(0)
	})

	return signal_handler.Start()
}

// Start starts the resource-gateway gRPC server
func (r *ResourceGatewayServer) Start() error {
	// Initialize the k8s client
	_, err := core.Instance().GetVersion()
	if err != nil {
		return fmt.Errorf("Unable to get k8s version: %v", err)
	}

	if sched.Instance() == nil {
		sched.Init(time.Second)
	}

	err = r.server.Start()
	if err != nil {
		return err
	}
	r.healthCheckServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	// Wait. The signal handler will exit cleanly
	logrus.Info("Px gRPC server running")
	select {}
}
