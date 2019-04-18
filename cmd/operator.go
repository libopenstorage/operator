package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/libopenstorage/operator/drivers/storage"
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/pkg/cluster"
	"github.com/libopenstorage/operator/pkg/controller"
	"github.com/libopenstorage/operator/pkg/version"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	api_v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	core_v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	componentconfig "k8s.io/kubernetes/pkg/apis/componentconfig/v1alpha1"
)

const (
	defaultLockObjectName      = "openstorage-operator"
	defaultLockObjectNamespace = "kube-system"
	eventComponentName         = "openstorage-operator"
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
			Name:  "lock-object-name",
			Usage: "Name for the lock object (default: stork)",
			Value: defaultLockObjectName,
		},
		cli.StringFlag{
			Name:  "lock-object-namespace",
			Usage: "Namespace for the lock object (default: kube-system)",
			Value: defaultLockObjectNamespace,
		},
		cli.BoolTFlag{
			Name:  "storage-cluster-controller",
			Usage: "Start the storage cluster controller (default: true)",
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

	k8sClient, err := clientset.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error getting client, %v", err)
	}

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&core_v1.EventSinkImpl{Interface: core_v1.New(k8sClient.CoreV1().RESTClient()).Events("")})
	recorder := eventBroadcaster.NewRecorder(legacyscheme.Scheme, api_v1.EventSource{Component: eventComponentName})

	runFunc := func(_ <-chan struct{}) {
		runOperator(d, recorder, c)
	}

	if c.BoolT("leader-elect") {
		lockObjectName := c.String("lock-object-name")
		lockObjectNamespace := c.String("lock-object-namespace")

		id, err := os.Hostname()
		if err != nil {
			log.Fatalf("Error getting hostname: %v", err)
		}

		lockConfig := resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		}

		resourceLock, err := resourcelock.New(
			resourcelock.ConfigMapsResourceLock,
			lockObjectNamespace,
			lockObjectName,
			k8sClient.CoreV1(),
			lockConfig)
		if err != nil {
			log.Fatalf("Error creating resource lock: %v", err)
		}

		defaultConfig := &componentconfig.LeaderElectionConfiguration{}
		componentconfig.SetDefaults_LeaderElectionConfiguration(defaultConfig)

		leaderElectionConfig := leaderelection.LeaderElectionConfig{
			Lock:          resourceLock,
			LeaseDuration: defaultConfig.LeaseDuration.Duration,
			RenewDeadline: defaultConfig.RenewDeadline.Duration,
			RetryPeriod:   defaultConfig.RetryPeriod.Duration,

			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: runFunc,
				OnStoppedLeading: func() {
					log.Fatalf("Openstorage operator lost master")
				},
			},
		}
		leaderElector, err := leaderelection.NewLeaderElector(leaderElectionConfig)
		if err != nil {
			log.Fatalf("Error creating leader elector: %v", err)
		}

		leaderElector.Run()
	} else {
		runFunc(nil)
	}
}

func runOperator(d storage.Driver, recorder record.EventRecorder, c *cli.Context) {
	if err := controller.Init(); err != nil {
		log.Fatalf("Error initializing controller: %v", err)
	}

	clusterController := cluster.Controller{
		Driver:   d,
		Recorder: recorder,
	}
	if err := clusterController.Init(); err != nil {
		log.Fatalf("Error initializing storage cluster controller: %v", err)
	}

	// The controller should be started at the end
	if err := controller.Run(); err != nil {
		log.Fatalf("Error starting controller: %v", err)
	}

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	for {
		<-signalChan
		log.Printf("Shutdown signal received, exiting...")
		if err := d.Stop(); err != nil {
			log.Warnf("Error stopping driver: %v", err)
		}
		os.Exit(0)
	}
}
