package main

import (
	"fmt"
	"log"
	_ "net/http/pprof"
	"os"
	"strings"

	"github.com/libopenstorage/grpc-framework/pkg/auth"
	grpcFramework "github.com/libopenstorage/grpc-framework/server"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	resourceGateway "github.com/libopenstorage/operator/pkg/resource-gateway"
	"github.com/libopenstorage/operator/pkg/version"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "resource-gateway"
	app.Usage = "gRPC service for managing resources"
	app.Version = version.Version
	app.Action = run

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "serverHost",
			Usage: "Host for resource-gateway gRPC server",
		},
		cli.StringFlag{
			Name:  "serverPort",
			Usage: "Port for resource-gateway gRPC server",
		},
		cli.StringFlag{
			Name:  "namespace",
			Usage: "Name of the configmap to use for semaphore",
		},
		cli.StringFlag{
			Name:  "configMapName",
			Usage: "Name of the configmap to use for semaphore",
		},
		cli.StringFlag{
			Name:  "configMapLabels",
			Usage: "Labels to use for the configmap",
		},
		cli.DurationFlag{
			Name:  "configMapUpdatePeriod",
			Usage: "Time period between configmap updates",
		},
		cli.DurationFlag{
			Name:  "deadClientTimeout",
			Usage: "Time period after which a node is considered dead",
		},
		cli.BoolFlag{
			Name:  "debug",
			Usage: "Set log level to debug",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting resource gateway gRPC server: %v", err)
	}
}

// run is the main function for resource-gateway gRPC server
// it initializes the k8s client, creates the gRPC server, and runs the server...
func run(c *cli.Context) {
	if c.Bool("debug") {
		logrus.SetLevel(logrus.DebugLevel)
	}

	resourceGatewayServer := resourceGateway.NewResourceGatewayServer(
		newResourceGatewayServerConfig(c),
		newSemaphoreConfig(c))
	err := resourceGatewayServer.SetupSigIntHandler()
	if err != nil {
		logrus.Fatalf("Failed to setup signal handler: %v", err)
	}
	err = resourceGatewayServer.Start()
	if err != nil {
		logrus.Fatalf("Failed to start resource-gateway server: %v", err)
	}
}

// newResourceGatewayServerConfig creates the config for resource-gateway gRPC server
func newResourceGatewayServerConfig(c *cli.Context) *grpcFramework.ServerConfig {
	resourceGatewayServerConfig := resourceGateway.NewResourceGatewayServerConfig()

	serverName := c.String("serverName")
	if serverName == "" {
		resourceGatewayServerConfig.Name = serverName
	}

	serverHost := c.String("serverHost")
	serverPort := c.String("serverPort")
	if serverHost != "" && serverPort != "" {
		serverAddress := fmt.Sprintf("%s:%s", serverHost, serverPort)
		resourceGatewayServerConfig.Address = serverAddress
	}

	// if Px security is enabled, then Issuer and SharedSecret will be set in the environment
	authIssuer := os.Getenv(pxutil.EnvKeyPortworxAuthJwtIssuer)
	authSharedSecret := os.Getenv(pxutil.EnvKeyPortworxAuthJwtSharedSecret)
	if authIssuer != "" && authSharedSecret != "" {
		security := &grpcFramework.SecurityConfig{}
		authenticator, err := auth.NewJwtAuthenticator(
			&auth.JwtAuthConfig{
				SharedSecret: []byte(authSharedSecret),
			})
		if err != nil {
			log.Fatalf("unable to create shared key authenticator")
		}
		security.Authenticators = map[string]auth.Authenticator{
			authIssuer: authenticator,
		}
		resourceGatewayServerConfig.Security = security
	}

	return resourceGatewayServerConfig
}

// newSemaphoreConfig creates a SemaphoreConfig object with provided
// cli arguments to initialize a new semaphore server
func newSemaphoreConfig(c *cli.Context) *resourceGateway.SemaphoreConfig {
	semaphoreConfig := resourceGateway.NewSemaphoreConfig()
	if c.String("configMapName") != "" {
		semaphoreConfig.ConfigMapName = c.String("configMapName")
	}
	if c.String("namespace") != "" {
		semaphoreConfig.ConfigMapNamespace = c.String("namespace")
	}
	if c.String("configMapLabels") != "" {
		configMapLabels := make(map[string]string)
		for _, kv := range strings.Split(c.String("configMapLabels"), ",") {
			kvSplit := strings.Split(kv, "=")
			if len(kvSplit) != 2 {
				logrus.Errorf("Invalid configMapLabels: %s", kvSplit)
				continue
			}
			configMapLabels[kvSplit[0]] = kvSplit[1]
		}
		semaphoreConfig.ConfigMapLabels = configMapLabels
	}
	if c.Duration("configMapUpdatePeriod") != 0 {
		semaphoreConfig.ConfigMapUpdatePeriod = c.Duration("configMapUpdatePeriod")
	}
	if c.Duration("deadClientTimeout") != 0 {
		semaphoreConfig.DeadClientTimeout = c.Duration("deadClientTimeout")
	}
	return semaphoreConfig
}
