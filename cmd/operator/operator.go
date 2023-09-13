package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/libopenstorage/operator/drivers/storage"
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	"github.com/libopenstorage/operator/pkg/apis"
	"github.com/libopenstorage/operator/pkg/controller/csr"
	"github.com/libopenstorage/operator/pkg/controller/portworxdiag"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	"github.com/libopenstorage/operator/pkg/controller/storagenode"
	_ "github.com/libopenstorage/operator/pkg/log"
	"github.com/libopenstorage/operator/pkg/migration"
	"github.com/libopenstorage/operator/pkg/operator-sdk/metrics"
	"github.com/libopenstorage/operator/pkg/preflight"
	"github.com/libopenstorage/operator/pkg/version"
	ocp_configv1 "github.com/openshift/api/config/v1"
	consolev1 "github.com/openshift/api/console/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	appsv1 "k8s.io/api/apps/v1"
	certv1 "k8s.io/api/certificates/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/flowcontrol"
	cluster_v1alpha1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	flagLeaderElect              = "leader-elect"
	flagMigration                = "migration"
	flagLeaderElectLockName      = "leader-elect-lock-name"
	flagLeaderElectLockNamespace = "leader-elect-lock-namespace"
	flagMetricsPort              = "metrics-port"
	flagRateLimiterQPS           = "rate-limiter-qps"
	flagRateLimiterBurst         = "rate-limiter-burst"
	flagEnableProfiling          = "pprof"
	flagEnableDiagController     = "diag-controller"
	flagDisableCacheFor          = "disable-cache-for"
	defaultLockObjectName        = "openstorage-operator"
	defaultResyncPeriod          = 30 * time.Second
	defaultMetricsPort           = 8999
	defaultPprofPort             = 6060
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
		cli.BoolTFlag{
			Name:  flagMigration,
			Usage: "Enable Portworx DaemonSet migration (default: true)",
		},
		cli.Float64Flag{
			Name:  flagRateLimiterQPS,
			Usage: "Set the rate limiter maximum QPS to the master from operator clients",
			Value: 0,
		},
		cli.IntFlag{
			Name:  flagRateLimiterBurst,
			Usage: "Set the rate limiter maximum burst for throttle",
			Value: 0,
		},
		cli.BoolFlag{
			Name:  flagEnableProfiling,
			Usage: "Enable Portworx Operator profiling using pprof (default: false)",
		},
		cli.BoolFlag{
			Name:  flagEnableDiagController,
			Usage: "Enable Portworx Diag Controller (default: false)",
		},
		cli.StringFlag{
			Name:  flagDisableCacheFor,
			Usage: "Comma separated object types to disable from cache to reduce memory usage, for example \"Pod,ConfigMap,Deployment,PersistentVolume\"",
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

	pprofEnabled := c.Bool(flagEnableProfiling)
	if pprofEnabled {
		go func() {
			log.Infof("pprof profiling is enabled, creating profiling server at port %v", defaultPprofPort)
			err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", defaultPprofPort), nil)
			if err != nil {
				log.Errorf("Error setting up profiling server. %v", err)
			}
		}()
	}

	diagControllerEnabled := c.Bool(flagEnableDiagController)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Error getting cluster config: %v", err)
	}

	// Set controller client rate limiter
	qps := c.Float64(flagRateLimiterQPS)
	burst := c.Int(flagRateLimiterBurst)
	if qps != 0 || burst != 0 {
		if qps == 0 || burst == 0 {
			log.Errorf("Rate limiter is not configured, both flags %s and %s need to be set", flagRateLimiterQPS, flagRateLimiterBurst)
		} else {
			log.Infof("Rate limiter is configured. QPS: %v, burst: %v", qps, burst)
			config.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(float32(qps), burst)
		}
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
		log.Fatalf("Error registering CRDs for StorageCluster controller: %v", err)
	}

	storageNodeController := storagenode.Controller{Driver: d}
	err = storageNodeController.RegisterCRD()
	if err != nil {
		log.Fatalf("Error registering CRDs for StorageNode controller: %v", err)
	}

	var diagController portworxdiag.Controller
	if diagControllerEnabled {
		diagController = portworxdiag.Controller{Driver: d}
		err = diagController.RegisterCRD()
		if err != nil {
			log.Fatalf("Error registering CRDs for PortworxDiag controller: %v", err)
		}
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

	if err := cluster_v1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := ocp_configv1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
	}

	if err := consolev1.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatalf("Failed to add cluster API resources to the scheme: %v", err)
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

	if err := preflight.InitPreflightChecker(mgr.GetClient()); err != nil {
		log.Fatalf("Error initializing preflight checker: %v", err)
	}

	if err := d.Init(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorderFor(storagecluster.ControllerName)); err != nil {
		log.Fatalf("Error initializing Storage driver %v: %v", driverName, err)
	}

	// init and start controllers
	if err := storageClusterController.Init(mgr); err != nil {
		log.Fatalf("Error initializing storage cluster controller: %v", err)
	}

	if err := storageNodeController.Init(mgr); err != nil {
		log.Fatalf("Error initializing storage node controller: %v", err)
	}

	certificateSigningRequestController := csr.Controller{}
	if err := certificateSigningRequestController.Init(mgr); err != nil {
		log.Fatalf("Error initializing certificate signing request controller: %v", err)
	}

	if diagControllerEnabled {
		if err := diagController.Init(mgr); err != nil {
			log.Fatalf("Error initializing portworx diag controller: %v", err)
		}
	}

	if err := storageClusterController.StartWatch(); err != nil {
		log.Fatalf("Error start watch on storage cluster controller: %v", err)
	}

	if err := storageNodeController.StartWatch(); err != nil {
		log.Fatalf("Error starting watch on storage node controller: %v", err)
	}

	if err := certificateSigningRequestController.StartWatch(); err != nil {
		log.Fatalf("Error starting watch on certificate signing request controller: %v", err)
	}

	if diagControllerEnabled {
		if err := diagController.StartWatch(); err != nil {
			log.Fatalf("Error starting watch on portworx diag controller: %v", err)
		}
	}

	if c.BoolT(flagMigration) {
		log.Info("Migration is enabled")
		migrationHandler := migration.New(&storageClusterController)
		go migrationHandler.Start()
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatalf("Manager exited non-zero error: %v", err)
	}
}

func getObjects(objectKinds string) []client.Object {
	var objs []client.Object

	if objectKinds == "" {
		return objs
	}

	arr := strings.Split(objectKinds, ",")
	for _, str := range arr {
		switch str {
		case "ConfigMap":
			objs = append(objs, &v1.ConfigMap{})
		case "Secret":
			objs = append(objs, &v1.Secret{})
		case "Pod":
			objs = append(objs, &v1.Pod{})
		case "Event":
			objs = append(objs, &v1.Event{})
		case "Service":
			objs = append(objs, &v1.Service{})
		case "ServiceAccount":
			objs = append(objs, &v1.ServiceAccount{})
		case "PersistentVolume":
			objs = append(objs, &v1.PersistentVolume{})
		case "PersistentVolumeClaim":
			objs = append(objs, &v1.PersistentVolumeClaim{})
		case "Deployment":
			objs = append(objs, &appsv1.Deployment{})
		case "DaemonSet":
			objs = append(objs, &appsv1.DaemonSet{})
		case "StatefulSet":
			objs = append(objs, &appsv1.StatefulSet{})
		case "ClusterRole":
			objs = append(objs, &rbacv1.ClusterRole{})
		case "ClusterRoleBinding":
			objs = append(objs, &rbacv1.ClusterRoleBinding{})
		case "Role":
			objs = append(objs, &rbacv1.Role{})
		case "RoleBinding":
			objs = append(objs, &rbacv1.RoleBinding{})
		case "StorageClass":
			objs = append(objs, &storagev1.StorageClass{})
		case "CertificateSigningRequest":
			objs = append(objs, &certv1.CertificateSigningRequest{})
		default:
			log.Warningf("Do not support disabling %s from cache.", str)
		}
	}

	log.Infof("%d object kinds will be disabled from cache", len(objs))
	return objs
}

func createManager(c *cli.Context, config *rest.Config) (manager.Manager, error) {
	syncPeriod := defaultResyncPeriod
	managerOpts := manager.Options{
		SyncPeriod:            &syncPeriod,
		MetricsBindAddress:    fmt.Sprintf("0.0.0.0:%d", c.Int(flagMetricsPort)),
		ClientDisableCacheFor: getObjects(c.String(flagDisableCacheFor)),
	}
	if c.BoolT(flagLeaderElect) {
		managerOpts.LeaderElection = true
		managerOpts.LeaderElectionID = c.String(flagLeaderElectLockName)
		managerOpts.LeaderElectionNamespace = c.String(flagLeaderElectLockNamespace)
		managerOpts.LeaderElectionResourceLock = resourcelock.ConfigMapsLeasesResourceLock
	}
	return manager.New(config, managerOpts)
}
