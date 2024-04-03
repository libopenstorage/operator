package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/rifflock/lfshook"
	"github.com/sirupsen/logrus"
	"io"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"log"
	"os"
	"path"
	"path/filepath"
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

	// New resource-specific flags
	var (
		colectPVsFlag          bool
		collectPVCsFlag        bool
		collectLogsForPodsFlag bool
		collectPXCentralFlag   bool
	)

	externalMode := false

	// Initialize flags
	flag.StringVar(&outputDirFlag, "output_dir", "/var/cores", "Output directory path. Specifies the directory where output files will be stored.")
	flag.StringVar(&kubeConfigFlag, "kubeconfig", "", "Path to the kubeconfig file. Specifies the path to the kubeconfig file to use for connection to the Kubernetes cluster.")
	flag.StringVar(&pxNamespaceFlag, "namespace", "portworx", "Portworx namespace. Specifies the Kubernetes namespace in which Portworx is running.")

	flag.BoolVar(&collectPVCsFlag, "collect_pvcs", false, "Set to true to collect PVCs.")
	flag.BoolVar(&colectPVsFlag, "collect_pvs", false, "Set to true to collect PVs.")
	flag.BoolVar(&collectLogsForPodsFlag, "collect_logs_for_pods", true, "Set to true to collect logs for pods.")
	flag.BoolVar(&collectPXCentralFlag, "collect_pxcentral", false, "Set to true to collect PX-Central/PX-Backup resources.")

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
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "  %s --namespace portworx --kubeconfig /path/to/kubeconfig --output_dir /var/cores --collect_pxcentral=true\n", os.Args[0])
		if err != nil {
			return
		}
		// print the default values for the boolean flags
		_, err = fmt.Fprintf(flag.CommandLine.Output(), "\nDefaults for boolean flags:\n --collect_pvcs=false\n --collect_pvs=false\n --collect_logs_for_pods=true\n --collect_pxcentral=false\n")
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
		err := k8sretriever.listPods(k8sretriever.pxNamespace, pod.Label, collectLogsForPodsFlag)
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

	if collectPVCsFlag {
		err = k8sretriever.listAllPVCs()
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error listing PVCs: %v", err)
			return
		}
	}

	if colectPVsFlag {
		err = k8sretriever.listAllPVs()
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error listing PVs: %v", err)
			return
		}
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

	// iterate through all namespaces and retrieve sharedv4 services with listSharedv4Services function
	namespaces, err := k8sretriever.k8sClient.CoreV1().Namespaces().List(k8sretriever.context, meta.ListOptions{})
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error listing namespaces: %v", err)
		return
	}

	for _, ns := range namespaces.Items {
		err = k8sretriever.listSharedv4Services(ns.Name)
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error listing sharedv4 services: %v", err)
			return
		}
	}

	if collectPXCentralFlag {
		err = k8sretriever.retrievePXCentralResources("")
		if err != nil {
			k8sretriever.loggerToUse.Errorf("Error retrieving PX-Central/PX-Backup resources: %v", err)
			return
		}
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

	// compress the directory logs into a tar.gz file
	err = k8sretriever.compressDirectoryLogs(k8sretriever.outputDir)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error compressing directory logs: %v", err)
		return
	}

	// delete the directory and its contents
	err = os.RemoveAll(k8sretriever.outputDir)
	if err != nil {
		k8sretriever.loggerToUse.Errorf("Error deleting directory: %v", err)
		return

	}

	k8sretriever.loggerToUse.Infof("Kubernetes objects and Portworx volume information have been successfully retrieved and stored in %s", k8sretriever.outputDir+".tar.gz")

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

		err := k8s.listPods(namespace, "", true)
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

// compressDirectoryLogs compresses the directory logs into a tar.gz file
func (k8s *k8sRetriever) compressDirectoryLogs(directory string) (err error) {

	tarFile, err := os.Create(directory + ".tar.gz")

	if err != nil {
		newError := fmt.Errorf("error creating tar file: %v", err)
		return newError
	}

	defer func(tarFile *os.File) {
		err := tarFile.Close()
		if err != nil {
			k8s.loggerToUse.Errorf("Failed to close file: %v", err)
		}
	}(tarFile)

	gzipWriter := gzip.NewWriter(tarFile)
	defer func(gzipWriter *gzip.Writer) {
		err := gzipWriter.Close()
		if err != nil {
			k8s.loggerToUse.Errorf("Failed to close gzip writer: %v", err)
		}
	}(gzipWriter)

	tarWriter := tar.NewWriter(gzipWriter)
	defer func(tarWriter *tar.Writer) {
		err := tarWriter.Close()
		if err != nil {
			k8s.loggerToUse.Errorf("Failed to close tar writer: %v", err)
		}
	}(tarWriter)

	dir := k8s.outputDir
	baseDir := filepath.Clean(dir) + string(filepath.Separator)

	err = filepath.Walk(dir, func(file string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symbolic links
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			k8s.loggerToUse.Infof("Skipping symbolic link: %s", file)
			return nil
		}

		// Create a relative path for files to preserve the directory structure
		relPath, err := filepath.Rel(baseDir, file)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(fileInfo, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !fileInfo.IsDir() {
			fileToTar, err := os.Open(file)
			if err != nil {
				return err
			}
			defer func(fileToTar *os.File) {
				err := fileToTar.Close()
				if err != nil {
					k8s.loggerToUse.Errorf("Failed to close file: %v", err)
				}
			}(fileToTar)

			// Copy the file data into the tar archive
			_, err = io.Copy(tarWriter, fileToTar)
			return err
		}

		return nil
	})

	if err != nil {
		k8s.loggerToUse.Errorf("Failed to compress directory logs: %v", err)
		return fmt.Errorf("error walking the path %v: %w", dir, err)
	}

	if err != nil {
		newError := fmt.Errorf("error walking the path: %v", err)
		return newError
	}

	return nil
}
