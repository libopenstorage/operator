package main

import (
	"fmt"
	"log"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

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
	// pxResourceGatewayStr is the common string for px-resource-gateway components
	pxResourceGatewayStr = "px-resource-gateway"

	// configuration values for px-resource-gateway server
	//
	// serverName is the service name for px-resource-gateway
	serverName = pxResourceGatewayStr
	// socket is the socket for px-resource-gateway
	socket = "/tmp/px-resource-gate.sock"

	// default values for semaphore server
	//
	// defaultServerHost is the default host for px-resource-gateway
	defaultServerHost = "127.0.0.1"
	// defaultServerPort is the default port for px-resource-gateway service
	defaultServerPort = "50051"
	// defaultConfigMapName is the default name for semaphore configmap
	defaultConfigMapName = pxResourceGatewayStr
	// defaultConfigMapNamespace is the default namespace to create semaphore configmap
	defaultConfigMapNamespace = "kube-system"
	// defaultConfigMapLabels are the default labels applied to semaphore configmap
	defaultConfigMapLabels = "name=px-resource-gateway"
	// defaultConfigMapUpdatePeriod is the default time period between configmap updates
	defaultConfigMapUpdatePeriod = 5 * time.Second
	// defaultDeadNodeTimeout is the default time period after which a node is considered dead
	defaultDeadNodeTimeout = 10 * time.Second
)

func main() {
	app := cli.NewApp()
	app.Name = "px-resource-gateway"
	app.Usage = "gRPC service for managing resources"
	app.Version = version.Version
	app.Action = run

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "serverHost",
			Usage: "Host for px-resource-gateway gRPC server",
			Value: defaultServerHost,
		},
		cli.StringFlag{
			Name:  "serverPort",
			Usage: "Port for px-resource-gateway gRPC server",
			Value: defaultServerPort,
		},
		cli.StringFlag{
			Name:  "configMapName",
			Usage: "Name of the configmap to use for semaphore",
			Value: defaultConfigMapName,
		},
		cli.StringFlag{
			Name:  "configMapNamespace",
			Usage: "Name of the configmap to use for semaphore",
			Value: defaultConfigMapNamespace,
		},
		cli.StringFlag{
			Name:  "configMapLabels",
			Usage: "Labels to use for the configmap",
			Value: defaultConfigMapLabels,
		},
		cli.DurationFlag{
			Name:  "configMapUpdatePeriod",
			Usage: "Time period between configmap updates",
			Value: defaultConfigMapUpdatePeriod,
		},
		cli.DurationFlag{
			Name:  "deadNodeTimeout",
			Usage: "Time period after which a node is considered dead",
			Value: defaultDeadNodeTimeout,
		},
		cli.BoolFlag{
			Name:  "debug",
			Usage: "Set log level to debug",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting px resource gateway gRPC server: %v", err)
	}
}

// run is the main function for px-resource-gateway gRPC server
// it initializes the k8s client, creates the gRPC server, and runs the server...
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

	// TODO: add security
	// create the config for px-resource-gateway gRPC server
	pxResourceGatewayServerConfig := &grpcFramework.ServerConfig{
		Name:         serverName,
		Address:      fmt.Sprintf("%s:%s", c.String("serverHost"), c.String("serverPort")),
		Socket:       socket,
		AuditOutput:  os.Stdout,
		AccessOutput: os.Stdout,
	}

	// register the various services with px-resource-gateway gRPC server
	healthCheckServer := health.NewServer()
	pxResourceGatewayServerConfig.
		RegisterGrpcServers(func(gs *grpc.Server) {
			pb.RegisterSemaphoreServiceServer(gs, newSemaphoreServer(c))
		}).
		RegisterGrpcServers(func(gs *grpc.Server) {
			healthpb.RegisterHealthServer(gs, healthCheckServer)
		}).
		WithDefaultGenericRoleManager()

	pxResourceGatewayServer, err := grpcFramework.New(pxResourceGatewayServerConfig)
	if err != nil {
		fmt.Printf("Unable to create server: %v", err)
		os.Exit(1)
	}

	// setup a signal handler to stop the server
	signal_handler := util.NewSigIntManager(func() {
		pxResourceGatewayServer.Stop()
		healthCheckServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		os.Remove(socket)
		os.Exit(0)
	})
	err = signal_handler.Start()
	if err != nil {
		fmt.Printf("Unable to start signal handler: %v", err)
		os.Exit(1)
	}

	// start the px-resource-gateway gRPC server
	err = pxResourceGatewayServer.Start()
	if err != nil {
		fmt.Printf("Unable to start server: %v", err)
		os.Exit(1)
	}
	healthCheckServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	// Wait. The signal handler will exit cleanly
	logrus.Info("Px gRPC server running")
	select {}
}

// newSemaphoreServer creates a SemaphoreConfig object with provided cli arguments
// to initialize a new semaphore server and returns the pb.SemaphoreServiceServer object
func newSemaphoreServer(c *cli.Context) pb.SemaphoreServiceServer {
	configMapLabels := make(map[string]string)
	for _, kv := range strings.Split(c.String("configMapLabels"), ",") {
		kvSplit := strings.Split(kv, "=")
		if len(kvSplit) != 2 {
			logrus.Errorf("Invalid configMapLabels: %s", kvSplit)
			continue
		}
		configMapLabels[kvSplit[0]] = kvSplit[1]
	}
	semaphoreServerConfig := &pxResourceGateway.SemaphoreConfig{
		ConfigMapName:         c.String("configMapName"),
		ConfigMapNamespace:    c.String("configMapNamespace"),
		ConfigMapLabels:       configMapLabels,
		ConfigMapUpdatePeriod: c.Duration("configMapUpdatePeriod"),
		DeadNodeTimeout:       c.Duration("deadNodeTimeout"),
	}
	semaphoreServer := pxResourceGateway.NewSemaphoreServer(semaphoreServerConfig)
	return semaphoreServer
}
