package main

import (
	"fmt"
	"log"
	_ "net/http/pprof"
	"os"

	"github.com/libopenstorage/grpc-framework/pkg/util"
	grpcFramework "github.com/libopenstorage/grpc-framework/server"
	pxResourceGateway "github.com/libopenstorage/operator/pkg/px-resource-gateway"
	"github.com/libopenstorage/operator/pkg/version"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const (
	// PxResourceGatewayString is the common string for px-resource-gateway components
	PxResourceGatewayString = "px-resource-gateway"
	// PxResourceGatewayServerName is the service name for px-resource-gateway
	PxResourceGatewayServerName = PxResourceGatewayString
	// PxResourceGatewayServerHost is the host for px-resource-gateway
	PxResourceGatewayServerHostEnv = "PX_RESOURCE_GATEWAY_HOST"
	// PxResourceGatewayServerHostDefault is the default host for px-resource-gateway
	PxResourceGatewayServerHostDefault = "127.0.0.1"
	// PxResourceGatewayServerPortEnv is the environment variable for px-resource-gateway service port
	PxResourceGatewayServerPortEnv = "PX_RESOURCE_GATEWAY_PORT"
	// PxResourceGatewayServerPortDefault is the default port for px-resource-gateway service
	PxResourceGatewayServerPortDefault = "50051"
	// PxResourceGatewayServerSocket is the socket for px-resource-gateway
	PxResourceGatewayServerSocket = "/tmp/px-resource-gate.sock"
)

func main() {
	app := cli.NewApp()
	app.Name = "openstorage-operator"
	app.Usage = "Operator to manage openstorage clusters"
	app.Version = version.Version
	app.Action = run

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "Set log level to debug",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting openstorage operator: %v", err)
	}
}

func run(c *cli.Context) {
	if c.Bool("debug") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	// TODO: expose method to initialize k8s client in sched-ops
	// initialize k8s client
	version, err := core.Instance().GetVersion()
	if err != nil {
		logrus.Fatalf("Failed to get k8s version: %v", err)
	}
	logrus.Debugf("Running on k8s version: %s", version.String())

	pxResourceGatewayServerHost := os.Getenv(PxResourceGatewayServerHostEnv)
	if pxResourceGatewayServerHost == "" {
		pxResourceGatewayServerHost = PxResourceGatewayServerHostDefault
	}
	pxResourceGatewayServerPort := os.Getenv(PxResourceGatewayServerPortEnv)
	if pxResourceGatewayServerPort == "" {
		pxResourceGatewayServerPort = PxResourceGatewayServerPortDefault
	}

	// TODO: add security
	pxResourceGatewayServerConfig := &grpcFramework.ServerConfig{
		Name:         PxResourceGatewayServerName,
		Address:      fmt.Sprintf("%s:%s", pxResourceGatewayServerHost, pxResourceGatewayServerPort),
		Socket:       PxResourceGatewayServerSocket,
		AuditOutput:  os.Stdout,
		AccessOutput: os.Stdout,
	}

	semaphoreServer := pxResourceGateway.NewSemaphoreServer()
	healthcheckServer := health.NewServer()
	pxResourceGatewayServerConfig.
		RegisterGrpcServers(func(gs *grpc.Server) {
			pb.RegisterSemaphoreServiceServer(gs, semaphoreServer)
		}).
		RegisterGrpcServers(func(gs *grpc.Server) {
			healthpb.RegisterHealthServer(gs, healthcheckServer)
		}).
		WithDefaultGenericRoleManager()

	pxResourceGatewayServer, err := grpcFramework.New(pxResourceGatewayServerConfig)
	if err != nil {
		fmt.Printf("Unable to create server: %v", err)
		os.Exit(1)
	}

	// Setup a signal handler
	signal_handler := util.NewSigIntManager(func() {
		pxResourceGatewayServer.Stop()
		healthcheckServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		os.Remove(PxResourceGatewayServerSocket)
		os.Exit(0)
	})
	err = signal_handler.Start()
	if err != nil {
		fmt.Printf("Unable to start signal handler: %v", err)
		os.Exit(1)
	}

	// start server
	healthcheckServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	err = pxResourceGatewayServer.Start()
	if err != nil {
		fmt.Printf("Unable to start server: %v", err)
		os.Exit(1)
	}

	// Wait. The signal handler will exit cleanly
	logrus.Info("Px gRPC server running")
	select {}
}
