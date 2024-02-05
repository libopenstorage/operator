package main

import (
	"context"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PodSelector is a struct to hold the name and label of a pod
type PodSelector struct {
	Name  string
	Label string
}

// objectProcessorFunc is a function to process an object
type objectProcessorFunc func(*unstructured.Unstructured) error

// k8sRetriever is a struct to hold the k8s retriever
type k8sRetriever struct {
	loggerToUse                 *logrus.Logger
	k8sClient                   client.Client
	restConfig                  *rest.Config
	context                     context.Context
	fs                          afero.Fs
	outputPath                  string
	scheme                      *runtime.Scheme
	containerProcessor          ContainerProcessor
	logFetcher                  LogFetcher
	yamlConverter               YamlConverter
	yamlEventsConverter         YamlConverter
	writeToFileVar              WriteToFileType
	podDetailsExtractor         PodDetailsExtractor
	logRequestCreator           LogRequestCreator
	restClient                  *rest.RESTClient
	handleDeploymentFunc        HandleDeploymentInterface
	handleStorageClusterFunc    HandleStorageClusterInterface
	handleKubernetesObjectsFunc HandleKubernetesObjectsInterface
}

// ContainerProcessor is an interface to process a container
type ContainerProcessor interface {
	ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, saveFilesPath string) error
}

// WriteToFileType is an interface to write to a file
type WriteToFileType interface {
	writeToFile(resourceName string, resourceType string, dataToProcess string, basePath string, fileExtensions ...string) error
}

// PodDetailsExtractor is an interface to extract pod details
type PodDetailsExtractor interface {
	extractPodDetails(pod *unstructured.Unstructured) (namespace string, podName string, nodeName string, podCreationTimestamp string, err error)
}

// LogRequestCreator is an interface to create a log request
type LogRequestCreator interface {
	CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error)
}

// Requests is a struct to hold the request
type Requests struct {
	Request K8sRequester
}

// K8sRequester is an interface to make a request to Kubernetes
type K8sRequester interface {
	DoRaw(ctx context.Context) ([]byte, error)
}

// HandleDeploymentInterface is an interface to handle a deployment
type HandleDeploymentInterface interface {
	HandleDeployment(obj *unstructured.Unstructured, path string) error
}

// HandleStorageClusterInterface is an interface to handle a storage cluster
type HandleStorageClusterInterface interface {
	HandleStorageCluster(obj *unstructured.Unstructured, path string) error
}

// RealHandleDeployment is a struct to handle a deployment
type RealHandleDeployment struct {
	k8s *k8sRetriever
}

// HandleDeployment handles a deployment
func (r *RealHandleDeployment) HandleDeployment(obj *unstructured.Unstructured, path string) error {
	return r.k8s.HandleDeployment(obj, path)
}

// RealHandleStorageCluster is a struct to handle a storage cluster
type RealHandleStorageCluster struct {
	k8s *k8sRetriever
}

// HandleStorageCluster handles a storage cluster
func (r *RealHandleStorageCluster) HandleStorageCluster(obj *unstructured.Unstructured, path string) error {
	return r.k8s.HandleStorageCluster(obj, path)
}

// YamlConverter is an interface to convert to YAML
type YamlConverter interface {
	ConvertToYaml(object interface{}) (string, error)
}

// RealYamlConverter is a struct to convert to YAML
type RealYamlConverter struct {
	k8s *k8sRetriever
}

// ConvertToYaml converts an object to YAML
func (r *RealYamlConverter) ConvertToYaml(object interface{}) (string, error) {
	return r.k8s.ConvertToYaml(object)
}

// RealWriteToFile is a struct to write to a file
type RealWriteToFile struct {
	fs afero.Fs
}

// LogFetcher is an interface to fetch logs
type LogFetcher interface {
	FetchLogs(pod *unstructured.Unstructured, containerName string, path string) error
}

// RealLogFetcher is a struct to fetch logs
type RealLogFetcher struct {
	k8s *k8sRetriever
}

// FetchLogs fetches logs
func (r *RealLogFetcher) FetchLogs(pod *unstructured.Unstructured, containerName string, path string) error {
	return r.k8s.FetchLogs(pod, containerName, path)
}

// RealContainerProcessor is a struct to process a container
type RealContainerProcessor struct {
	k8s *k8sRetriever
}

// ProcessContainer processes a container
func (r *RealContainerProcessor) ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, saveFilesPath string) error {
	return r.k8s.ProcessContainer(pod, containerItem, saveFilesPath)
}

// RealPodDetailsExtractor is a struct to extract pod details
type RealPodDetailsExtractor struct {
	k8s *k8sRetriever
}

// extractPodDetails extracts pod details
func (r *RealPodDetailsExtractor) extractPodDetails(pod *unstructured.Unstructured) (namespace string, podName string, nodeName string, podCreationTimestamp string, err error) {
	return r.k8s.extractPodDetails(pod)
}

// RealLogRequestCreator is a struct to create a log request
type RealLogRequestCreator struct {
	k8s *k8sRetriever
}

// CreateLogsRequest creates a log request
func (r *RealLogRequestCreator) CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error) {
	return r.k8s.CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp)
}

// KubernetesConfigProvider is an interface to provide the k8s config
type KubernetesConfigProvider interface {
	InClusterConfig() (*rest.Config, error)
}

// RealKubernetesConfigProvider is a struct to provide the k8s config
type RealKubernetesConfigProvider struct{}

// InClusterConfig provides the k8s config
func (rkcp *RealKubernetesConfigProvider) InClusterConfig() (*rest.Config, error) {
	return rest.InClusterConfig()
}

// HandleKubernetesObjectsInterface is an interface to handle k8s objects
type HandleKubernetesObjectsInterface interface {
	HandleKubernetesObjects(obj *unstructured.Unstructured, path string, objectName string) error
}

// RealHandleKubernetesObjects is a struct to handle k8s objects
type RealHandleKubernetesObjects struct {
	k8s *k8sRetriever
}

// HandleKubernetesObjects handles k8s objects
func (r *RealHandleKubernetesObjects) HandleKubernetesObjects(obj *unstructured.Unstructured, path string, objectName string) error {
	return r.k8s.HandleKubernetesObjects(obj, path, objectName)

}

// k8sObjectParams is a struct to hold the parameters for a k8s object
type k8sObjectParams struct {
	GVK          schema.GroupVersionKind
	ListOpts     []client.ListOption
	ResourceName string
	Namespace    string
}
