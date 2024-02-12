package main

import (
	"context"
	"flag"
	"fmt"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/sirupsen/logrus"
	"io"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"log"
	"os"
	"path"
	"syscall"
	"time"
)

// k8sRetriever is a struct that implements the Retriever interface
type k8sRetriever struct {
	loggerToUse    *logrus.Logger
	k8sClient      *kubernetes.Clientset
	restConfig     *rest.Config
	context        context.Context
	outputDir      string
	kubeConfigFile string
	pxNamespace    string
}

// setupLogging sets up logging to a file and returns a logrus logger
func setupLogging(pathOfLog string) *logrus.Logger {
	logPath := pathOfLog + "/k8s-retriever.log"
	err := os.MkdirAll(path.Dir(logPath), os.ModePerm)
	if err != nil {
		return nil
	}

	// setting logrus logger
	logger := logrus.New()

	logger.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	})

	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			logger.Errorf("Failed to close file: %v", err)
		}
	}(logger.Out.(*os.File))

	logWriter, err := rotatelogs.New(
		logPath+".%Y%m%d%H%M",
		rotatelogs.WithLinkName(logPath),          // generate softlink
		rotatelogs.WithMaxAge(24*time.Hour*60),    // max age, 60 days
		rotatelogs.WithRotationTime(time.Hour*24), // log cut, 24 hours
		rotatelogs.WithRotationSize(20*1024*1024), // enforce rotation when the log size reaches 20MB
	)
	if err != nil {
		log.Fatalf("Failed to create rotatelogs: %v", err)
	}

	writeMap := lfshook.WriterMap{
		logrus.InfoLevel:  logWriter,
		logrus.FatalLevel: logWriter,
		logrus.DebugLevel: logWriter,
		logrus.WarnLevel:  logWriter,
		logrus.ErrorLevel: logWriter,
		logrus.PanicLevel: logWriter,
	}

	lfHook := lfshook.NewHook(writeMap, &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
	})

	// add hook to logger, this writes to the log file located at logPath
	logger.AddHook(lfHook)

	// add hook to logger, this writes to stdout
	logger.SetOutput(io.MultiWriter(os.Stdout))

	return logger
}

// main function, we will create a Deployment and a PVC with a Portworx Sharedv4 volume
func main() {

	// Define flags
	var (
		outputDirFlag   string
		kubeConfigFlag  string
		pxNamespaceFlag string
	)

	externalMode := false

	// Initialize flags
	flag.StringVar(&outputDirFlag, "output_dir", "/var/cores", "Output directory path. Specifies the directory where output files will be stored.")
	flag.StringVar(&kubeConfigFlag, "kubeconfig", "", "Path to the kubeconfig file. Specifies the path to the kubeconfig file to use for connection to the Kubernetes cluster.")
	flag.StringVar(&pxNamespaceFlag, "namespace", "portworx", "Portworx namespace. Specifies the Kubernetes namespace in which Portworx is running.")

	// Custom usage function
	flag.Usage = func() {
		_, err := fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "This tool retrieves Kubernetes objects and Portworx volume information.\n\n")
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "Flags:\n")
		if err != nil {
			return
		}
		flag.PrintDefaults()
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n")
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "  %s --namespace portworx --kubeconfig /path/to/kubeconfig --output_dir /var/cores\n", os.Args[0])
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "  %s --namespace kube-system --output_dir /tmp\n", os.Args[0])
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "\nPlease specify --namespace, --kubeconfig (if connecting outside the cluster), and --output_dir when running the tool.\n")
		if err != nil {
			return
		}
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {

		// If the KUBERNETES_SERVICE_HOST environment variable is not set, then we are running in external mode
		externalMode = true

	}

	// Parse the command-line flags
	flag.Parse()

	if kubeConfigFlag == "" && externalMode { // Example validation for required flag in external mode
		fmt.Println("Error: --kubeconfig is required for external mode.")
		flag.Usage()
		os.Exit(1)
	}

	if outputDirFlag == "" {
		fmt.Println("Error: --output_dir is required.")
		flag.Usage()
		return
	}

	k8sretriever := k8sRetriever{
		context:        context.Background(),
		outputDir:      outputDirFlag,
		kubeConfigFile: kubeConfigFlag,
		pxNamespace:    pxNamespaceFlag,
	}

	// Get the current umask
	oldUmask := syscall.Umask(0)

	// Print current user id
	log.Printf("Current user id: %d", syscall.Getuid())

	// Print current group id
	log.Printf("Current group id: %d", syscall.Getgid())

	k8sretriever.outputDir = outputDirFlag + "/px-k8s-retriever"

	err := os.MkdirAll(k8sretriever.outputDir, 0766)
	if err != nil {
		log.Fatalf("failed to create log directory for px-retriever: %s", err.Error())
		return
	}

	// Print current directory permissions
	fi, err := os.Stat(k8sretriever.outputDir)
	if err != nil {
		log.Fatalf("Failed to get file log info: %s", err.Error())
		return
	}

	log.Printf("Current directory %s permissions: %o", k8sretriever.outputDir, fi.Mode().Perm())

	log.Printf("Current directory %s owner: %d", k8sretriever.outputDir, fi.Sys().(*syscall.Stat_t).Uid)

	// Reset it back immediately so that it only affects this process for a short duration
	defer syscall.Umask(oldUmask)

	logger := setupLogging(k8sretriever.outputDir)

	k8sretriever.loggerToUse = logger

	if externalMode {
		k8sretriever.loggerToUse.Infof("Using out-of-cluster configuration")
		k8sretriever.loggerToUse.Infof("Kubeconfig file: %s", k8sretriever.kubeConfigFile)

		// Use the provided kubeconfig file
		clientset, restConfig, err := getClientExternal(k8sretriever.kubeConfigFile)
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error creating clientset: %v", err)
			return
		}

		k8sretriever.k8sClient = clientset
		k8sretriever.restConfig = restConfig

	} else {
		k8sretriever.loggerToUse.Infof("Running internally, using in-cluster configuration")

		// We are running inside a cluster
		clientset, restConfig, err := getClientInternal()
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error creating clientset: %v", err)
			return
		}

		k8sretriever.k8sClient = clientset
		k8sretriever.restConfig = restConfig

	}

	if k8sretriever.pxNamespace == "" {
		k8sretriever.pxNamespace = "portworx"
	}

	// nodeName is the name of the Kubernetes node where the job is running
	var nodeName = os.Getenv("NODE_NAME")

	if nodeName == "" {
		nodeName, _ = os.Hostname()
	}

	k8sretriever.loggerToUse.Infof("---Starting Kubernetes client!---")
	k8sretriever.loggerToUse.Infof("Job is running on Node: %s", nodeName)

	dc, err := k8sretriever.createDynamicClient()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error creating dynamic client: %v", err)
		return
	}

	if k8sretriever.isOpenShift(dc) {
		k8sretriever.loggerToUse.Infof("OpenShift cluster detected, patching SCC")
		err = k8sretriever.patchOpenShiftSCC(dc, k8sretriever.pxNamespace, "privileged")
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error patching SCC: %v", err)
			return
		}
	} else {
		k8sretriever.loggerToUse.Infof("Not an OpenShift cluster, skipping SCC patch")
	}

	k8sretriever.loggerToUse.Infof("Creating YAML files for all resources in namespace %s", k8sretriever.pxNamespace)

	k8sretriever.loggerToUse.Infof("YAML files will be created in %s", k8sretriever.outputDir)

	err = k8sretriever.listDeployments(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing deployments: %v", err)
		return
	}

	// PodSelector is a struct that contains the name and label of a pod
	type PodSelector struct {
		Name  string
		Label string
	}

	listOfPods := []PodSelector{
		{Name: "portworx", Label: "name=portworx"},
		{Name: "portworx-api", Label: "name=portworx-api"},
		{Name: "portworx-kvdb", Label: "kvdb=true"},
		{Name: "portworx-operator", Label: "name=portworx-operator"},
		{Name: "portworx-pvc-controller", Label: "name=portworx-pvc-controller"},
		{Name: "prometheus-px-prometheus", Label: "prometheus=px-prometheus"},
		{Name: "px-csi-ext", Label: "app=px-csi-driver"},
		{Name: "px-plugin", Label: "app=px-plugin"},
		{Name: "px-plugin-proxy", Label: "name=px-plugin-proxy"},
		{Name: "px-telemetry-phonehome", Label: "name=px-telemetry-phonehome"},
		{Name: "stork", Label: "name=stork"},
		{Name: "stork-scheduler", Label: "name=stork-scheduler"},
	}

	for _, pod := range listOfPods {
		k8sretriever.loggerToUse.Printf("Pod: %s\n", pod.Name)
		err := k8sretriever.listPods(k8sretriever.pxNamespace, pod.Label)
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error listing pods: %v", err)
			return
		}
	}

	err = k8sretriever.listServices(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing services: %v", err)
		return
	}

	err = k8sretriever.listStorageClusters(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing storage-clusters: %v", err)
		return
	}

	err = k8sretriever.listStorageNodes(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing storage-nodes: %v", err)
		return
	}

	err = k8sretriever.listStatefulSets(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing stateful-sets: %v", err)
		return
	}

	err = k8sretriever.listDaemonSets(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing daemon-sets: %v", err)
		return
	}

	err = k8sretriever.listAllPVCs()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing PVCs: %v", err)
		return
	}

	err = k8sretriever.listAllPVs()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing PVs: %v", err)
		return
	}

	err = k8sretriever.listKubernetesWorkerNodes()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing Kubernetes worker nodes: %v", err)
		return
	}

	err = k8sretriever.listStorageClasses()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing StorageClasses: %v", err)
		return
	}

	err = k8sretriever.listStorageProfiles()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing StorageProfiles: %v", err)
		return
	}

	err = k8sretriever.listClusterPairs()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing ClusterPairs: %v", err)
		return
	}

	err = k8sretriever.listBackupLocations()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing BackupLocations: %v", err)
		return
	}

	err = k8sretriever.listVolumePlacementStrategies()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing VolumePlacementStrategies: %v", err)
		return
	}

	err = k8sretriever.listVolumeSnapshotClasses()
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing VolumeSnapshotClasses: %v", err)
		return
	}

	err = k8sretriever.getPXConfigMapsKubeSystem("px-bootstrap")
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error getting px-bootstrap configmap: %v", err)
		return
	}

	err = k8sretriever.getPXConfigMapsKubeSystem("px-cloud-drive")
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error getting px-cloud-drive configmap: %v", err)
		return
	}

	err = k8sretriever.listConfigMaps(k8sretriever.pxNamespace)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing configmaps: %v", err)
		return
	}

	err = k8sretriever.retrievePXCentralResources("")
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error retrieving PX-Central/PX-Backup resources: %v", err)
		return
	}

	err = k8sretriever.deleteEmptyDirs(k8sretriever.outputDir)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error deleting empty directories: %v", err)
		return
	}

	if k8sretriever.isOpenShift(dc) {
		err = k8sretriever.removeOpenShiftSCC(dc, k8sretriever.pxNamespace, "privileged")
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error removing SCC: %v", err)
			return
		}
	}

}

// retrievePXCentralResources retrieves PX-Central/PX-Backup resources if they exist
func (k8s *k8sRetriever) retrievePXCentralResources(pxcentralns string) error {

	var existingNamespaces []string

	if pxcentralns != "" {
		existingNamespaces = append(existingNamespaces, pxcentralns)
	} else {
		if k8s.namespaceExists("px-backup") {
			existingNamespaces = append(existingNamespaces, "px-backup")
		}

		if k8s.namespaceExists("px-central") {
			existingNamespaces = append(existingNamespaces, "px-central")
		}

		if len(existingNamespaces) == 0 {

			k8s.loggerToUse.Infof("PX-Central/PX-Backup resources not found in any namespace")
			return nil

		}
	}

	// save the original path
	originalPath := k8s.outputDir
	// append to output directory px-central
	k8s.outputDir = k8s.outputDir + "/px-central"

	k8s.loggerToUse.Infof("Retrieving PX-Central/PX-Backup resources...")

	for _, namespace := range existingNamespaces {

		err := k8s.listPods(namespace, "")
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing pods: %v", err)
			return err
		}

		err = k8s.listDeployments(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing deployments: %v", err)
			return err

		}

		err = k8s.listStatefulSets(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing stateful-sets: %v", err)
			return err

		}

		err = k8s.listDaemonSets(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing daemon-sets: %v", err)
			return err
		}

		err = k8s.listJobs(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing jobs: %v", err)
			return err

		}

		err = k8s.listServices(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing services: %v", err)
			return err
		}

		err = k8s.listConfigMaps(namespace)
		if err != nil {
			k8s.loggerToUse.Errorf("Error listing configmaps: %v", err)
			return err
		}

	}

	// change back to the original path
	k8s.outputDir = originalPath

	return nil
}
