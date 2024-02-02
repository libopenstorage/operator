// client.go contains the main function and the initialization of the k8sRetriever
package main

import (
	"context"
	"fmt"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"io"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"os"
	"path"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"syscall"
)

var listOfPods = []PodSelector{
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

// setupLogging sets up logging to a file and returns a logrus logger
func setupLogging() *logrus.Logger {

	// Get the current umask
	oldUmask := syscall.Umask(0)

	// Print current user id
	log.Printf("Current user id: %d", syscall.Getuid())

	// Print current group id
	log.Printf("Current group id: %d", syscall.Getgid())

	logPath := outputPath + "/k8s-retriever.log"
	err := os.MkdirAll(path.Dir(logPath), 0766)
	if err != nil {
		log.Fatalf("failed to create log directory since the logger: %s", err.Error())
		return nil
	}

	// Print current directory permissions
	fi, err := os.Stat(outputPath)
	if err != nil {
		log.Fatalf("Failed to get file info: %s", err.Error())
		return nil
	}
	log.Printf("Current directory %s permissions: %o", outputPath, fi.Mode().Perm())

	log.Printf("Current directory %s owner: %d", outputPath, fi.Sys().(*syscall.Stat_t).Uid)

	// Reset it back immediately so that it only affects this process for a short duration
	defer syscall.Umask(oldUmask)

	// setting logrus logger
	logger := logrus.New()

	logger.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	})

	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			logger.Errorf("failed to close file: %v", err)
		}
	}(logger.Out.(*os.File))

	// Setting the output for logrus, and optionally to stdout
	logger.SetOutput(io.MultiWriter(os.Stdout))

	return logger
}

// initializeRetriever initializes the k8sRetriever
func initializeRetriever(logger *logrus.Logger, k8sClient client.Client, fs afero.Fs) (*k8sRetriever, error) {

	k8sRetrieverIns := &k8sRetriever{
		loggerToUse: logger,
		context:     context.Background(),
		fs:          fs,
		outputPath:  outputPath,
		scheme:      runtime.NewScheme(),
	}

	if k8sClient == nil {
		clientK8s, err := k8s.NewK8sClient(k8sRetrieverIns.scheme)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Kubernetes client: %v", err)
		}
		k8sRetrieverIns.k8sClient = clientK8s

	}

	k8sRetrieverIns.handleDeploymentFunc = &RealHandleDeployment{k8s: k8sRetrieverIns}
	k8sRetrieverIns.handleStorageClusterFunc = &RealHandleStorageCluster{k8s: k8sRetrieverIns}
	k8sRetrieverIns.handleKubernetesObjectsFunc = &RealHandleKubernetesObjects{k8s: k8sRetrieverIns}

	k8sRetrieverIns.yamlConverter = &RealYamlConverter{
		k8s: k8sRetrieverIns,
	}
	k8sRetrieverIns.writeToFileVar = &RealWriteToFile{fs: k8sRetrieverIns.fs}
	k8sRetrieverIns.logFetcher = &RealLogFetcher{k8s: k8sRetrieverIns}
	k8sRetrieverIns.yamlEventsConverter = &RealYamlConverter{
		k8s: k8sRetrieverIns,
	}
	k8sRetrieverIns.containerProcessor = &RealContainerProcessor{
		k8s: k8sRetrieverIns,
	}
	k8sRetrieverIns.podDetailsExtractor = &RealPodDetailsExtractor{
		k8s: k8sRetrieverIns,
	}
	k8sRetrieverIns.logRequestCreator = &RealLogRequestCreator{
		k8s: k8sRetrieverIns,
	}

	// Set the k8s config

	err := setK8sConfig(k8sRetrieverIns, &RealKubernetesConfigProvider{})
	if err != nil {
		return nil, err
	}

	log.Printf("K8s config set successfully")

	return k8sRetrieverIns, nil
}

// setK8sConfig sets the k8s config for the k8sRetriever
func setK8sConfig(k8sRetrieverIns *k8sRetriever, configProvider KubernetesConfigProvider) error {
	var err error
	if kubeconfigPath != "" {
		k8sRetrieverIns.restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return fmt.Errorf("failed to get out-of-cluster config: %v", err)
		}
	} else if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != "" {
		k8sRetrieverIns.restConfig, err = configProvider.InClusterConfig()
		if err != nil {
			return fmt.Errorf("failed to get in-cluster config: %v", err)
		}
	} else {
		return fmt.Errorf("cannot determine running environment. Neither KUBECONFIG is set nor running inside a pod")
	}

	k8sRetrieverIns.loggerToUse.Infof("K8s config set successfully")

	return nil
}