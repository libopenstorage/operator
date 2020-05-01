package main

import (
	"context"
	"fmt"
	"os"
	"time"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/libopenstorage/operator/drivers/storage"
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/pkg/apis"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	_ "github.com/libopenstorage/operator/pkg/log"
	"github.com/libopenstorage/operator/pkg/version"
	"github.com/operator-framework/operator-sdk/pkg/metrics"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/staging/src/k8s.io/sample-controller/pkg/signals"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	flagLeaderElect              = "leader-elect"
	flagLeaderElectLockName      = "leader-elect-lock-name"
	flagLeaderElectLockNamespace = "leader-elect-lock-namespace"
	flagMetricsPort              = "metrics-port"
	defaultLockObjectName        = "openstorage-operator"
	defaultResyncPeriod          = 30 * time.Second
	defaultMetricsPort           = 8999
	metricsPortName              = "metrics"
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
		cli.StringFlag{
			Name:  "driver,d",
			Usage: "Storage driver name",
		},
		cli.BoolTFlag{
			Name:  flagLeaderElect,
			Usage: "Enable leader election (default: true)",
		},
		cli.StringFlag{
			Name:  flagLeaderElectLockName,
			Usage: "Name for the leader election lock object",
			Value: defaultLockObjectName,
		},
		cli.StringFlag{
			Name:  flagLeaderElectLockNamespace,
			Usage: "Namespace for the leader election lock object",
		},
		cli.IntFlag{
			Name:  flagMetricsPort,
			Usage: "Port on which the operator metrics are to be exposed",
			Value: defaultMetricsPort,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatalf("Error starting openstorage operator: %v", err)
	}
}

func run(c *cli.Context) {
	log.Infof("Starting openstorage operator version %v", version.Version)
	driverName := c.String("driver")
	if len(driverName) == 0 {
		log.Fatalf("driver option is required")
	}

	verbose := c.Bool("verbose")
	if verbose {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error getting cluster config: %v", err)
	}

	// Register CRDs
	log.Info("Registering components")

	d, err := storage.Get(driverName)
	if err != nil {
		log.Fatalf("Error getting Storage driver %v: %v", driverName, err)
	}

	storageClusterController := storagecluster.Controller{Driver: d}
	err = storageClusterController.RegisterCRD()
	if err != nil {
		log.Fatalf("Error registering CRD's for StorageCluster controller: %v", err)
	}

	// TODO: Don't move createManager above register CRD section. This part will be refactored because of a bug,
	// similar to https://github.com/kubernetes-sigs/controller-runtime/issues/321
	mgr, err := createManager(c, config)
	if err != nil {
		log.Fatalf("Failed to create controller manager: %v", err)
	}

	// Add custom resources to scheme
	// TODO: AddToScheme should strictly follow createManager, after CRDs are registered. See comment above
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add resources to the scheme: %v", err)
	}
	if err := monitoringv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add prometheus resources to the scheme: %v", err)
	}

	// Create Service and ServiceMonitor objects to expose the metrics to Prometheus
	metricsPort := c.Int(flagMetricsPort)
	metricsServicePorts := []v1.ServicePort{
		{
			Name:       metricsPortName,
			Port:       int32(metricsPort),
			TargetPort: intstr.FromInt(metricsPort),
		},
	}
	metricsService, err := metrics.CreateMetricsService(context.TODO(), config, metricsServicePorts)

	if err == nil {
		if metricsService == nil {
			log.Info("Skipping metrics Service creation; not running in a cluster.")
		} else {
			_, err = metrics.CreateServiceMonitors(config, metricsService.Namespace, []*v1.Service{metricsService})
			if err != nil && !errors.IsAlreadyExists(err) {
				log.Warnf("Failed to create ServiceMonitor: %v", err)
			}
		}
	} else {
		log.Warnf("Failed to expose metrics port: %v", err)
	}

	if err = d.Init(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorderFor(storagecluster.ControllerName)); err != nil {
		log.Fatalf("Error initializing Storage driver %v: %v", driverName, err)
	}

	// Setup storage cluster controller
	if err := storageClusterController.Init(mgr); err != nil {
		log.Fatalf("Error initializing storage cluster controller: %v", err)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Fatalf("Manager exited non-zero error: %v", err)
	}
}

func createManager(c *cli.Context, config *rest.Config) (manager.Manager, error) {
	syncPeriod := defaultResyncPeriod
	managerOpts := manager.Options{
		SyncPeriod:         &syncPeriod,
		MetricsBindAddress: fmt.Sprintf("0.0.0.0:%d", c.Int(flagMetricsPort)),
	}
	if c.BoolT(flagLeaderElect) {
		managerOpts.LeaderElection = true
		managerOpts.LeaderElectionID = c.String(flagLeaderElectLockName)
		managerOpts.LeaderElectionNamespace = c.String(flagLeaderElectLockNamespace)
	}
	return manager.New(config, managerOpts)
}
