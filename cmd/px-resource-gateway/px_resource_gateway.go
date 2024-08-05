package main

import (
	"context"
	"fmt"
	_ "net/http/pprof"
	"os"

	"github.com/libopenstorage/grpc-framework/pkg/util"
	grpcServer "github.com/libopenstorage/grpc-framework/server"
	server "github.com/libopenstorage/operator/pkg/px-resource-gateway"
	"github.com/libopenstorage/operator/pkg/version"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"google.golang.org/grpc"
)

const (
	socket = "/tmp/px-resource-gate.sock"
)

func main() {
	app := cli.NewApp()
	app.Name = "openstorage-operator"
	app.Usage = "Operator to manage openstorage clusters"
	app.Version = version.Version
	app.Action = run

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "verbose",
			Usage: "Enable verbose logging",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting openstorage operator: %v", err)
	}
}

func run(c *cli.Context) {
	logrus.SetLevel(logrus.DebugLevel)
	semaphoreServer := server.NewServer()

	// TODO: add security
	config := &grpcServer.ServerConfig{
		Name:         "px-resource-gate",
		Address:      "127.0.0.1:9009",
		Socket:       socket,
		AuditOutput:  os.Stdout,
		AccessOutput: os.Stdout,
	}
	config.
		RegisterGrpcServers(func(gs *grpc.Server) {
			pb.RegisterSemaphoreServiceServer(gs, semaphoreServer)
		}).
		WithServerUnaryInterceptors(
			gRPCInterceptor,
		).
		WithDefaultGenericRoleManager()

	// Create grpc framework server
	os.Remove(socket)
	s, err := grpcServer.New(config)
	if err != nil {
		fmt.Printf("Unable to create server: %v", err)
		os.Exit(1)
	}

	// Setup a signal handler
	signal_handler := util.NewSigIntManager(func() {
		s.Stop()
		os.Remove(socket)
		os.Exit(0)
	})
	signal_handler.Start()

	// Start server
	err = s.Start()
	if err != nil {
		fmt.Printf("Unable to start server: %v", err)
		os.Exit(1)
	}

	// Wait. The signal handler will exit cleanly
	logrus.Info("Px gRPC server running")
	select {}
}

func gRPCInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	logrus.Info("In Px gRPC interceptor")

	return handler(ctx, req)
}
