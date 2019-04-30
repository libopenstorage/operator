package main

import (
	"flag"
	"os"
	"time"

	"github.com/libopenstorage/operator/drivers/storage"
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/pkg/apis"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/version"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/staging/src/k8s.io/sample-controller/pkg/signals"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	defaultLockObjectName      = "openstorage-operator"
	defaultLockObjectNamespace = "kube-system"
	defaultResyncPeriod        = 30 * time.Second
)

func main() {
	err := flag.CommandLine.Parse([]string{})
	if err != nil {
		log.Warnf("Error parsing flag: %v", err)
	}
	err = flag.Set("logtostderr", "true")
	if err != nil {
		log.Fatalf("Error setting glog flag: %v", err)
	}

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
			Name:  "leader-elect",
			Usage: "Enable leader election (default: true)",
		},
		cli.StringFlag{
			Name:  "leader-elect-lock-name",
			Usage: "Name for the leader election lock object",
			Value: defaultLockObjectName,
		},
		cli.StringFlag{
			Name:  "leader-elect-lock-namespace",
			Usage: "Namespace for the leader election lock object",
			Value: defaultLockObjectNamespace,
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
	}

	d, err := storage.Get(driverName)
	if err != nil {
		log.Fatalf("Error getting Storage driver %v: %v", driverName, err)
	}

	if err = d.Init(nil); err != nil {
		log.Fatalf("Error initializing Storage driver %v: %v", driverName, err)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error getting cluster config: %v", err)
	}

	syncPeriod := defaultResyncPeriod
	managerOpts := manager.Options{
		SyncPeriod: &syncPeriod,
	}
	if c.BoolT("leader-elect") {
		managerOpts.LeaderElection = true
		managerOpts.LeaderElectionID = c.String("leader-elect-lock-name")
		managerOpts.LeaderElectionNamespace = c.String("leader-elect-lock-namespace")
	}
	mgr, err := manager.New(config, managerOpts)
	if err != nil {
		log.Fatalf("Failed to create controller manager: %v", err)
	}

	log.Info("Registering components")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add resources to the scheme: %v", err)
	}

	// Setup storage cluster controller
	clusterController := storagecluster.Controller{Driver: d}
	if err := clusterController.Init(mgr); err != nil {
		log.Fatalf("Error initializing storage cluster controller: %v", err)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited non-zero")
		os.Exit(1)
	}
}
