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

type PodSelector struct {
	Name  string
	Label string
}

type objectProcessorFunc func(*unstructured.Unstructured) error

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

type ContainerProcessor interface {
	ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, saveFilesPath string) error
}

type WriteToFileType interface {
	writeToFile(resourceName string, resourceType string, dataToProcess string, basePath string, fileExtensions ...string) error
}

type PodDetailsExtractor interface {
	extractPodDetails(pod *unstructured.Unstructured) (namespace string, podName string, nodeName string, podCreationTimestamp string, err error)
}

type LogRequestCreator interface {
	CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error)
}

type Requests struct {
	Request K8sRequester
}

type K8sRequester interface {
	DoRaw(ctx context.Context) ([]byte, error)
}

type HandleDeploymentInterface interface {
	HandleDeployment(obj *unstructured.Unstructured, path string) error
}

type HandleStorageClusterInterface interface {
	HandleStorageCluster(obj *unstructured.Unstructured, path string) error
}

type RealHandleDeployment struct {
	k8s *k8sRetriever
}

func (r *RealHandleDeployment) HandleDeployment(obj *unstructured.Unstructured, path string) error {
	return r.k8s.HandleDeployment(obj, path)
}

type RealHandleStorageCluster struct {
	k8s *k8sRetriever
}

func (r *RealHandleStorageCluster) HandleStorageCluster(obj *unstructured.Unstructured, path string) error {
	return r.k8s.HandleStorageCluster(obj, path)
}

type YamlConverter interface {
	ConvertToYaml(object interface{}) (string, error)
}

type RealYamlConverter struct {
	k8s *k8sRetriever
}

func (r *RealYamlConverter) ConvertToYaml(object interface{}) (string, error) {
	return r.k8s.ConvertToYaml(object)
}

type RealWriteToFile struct {
	fs afero.Fs
}

type LogFetcher interface {
	FetchLogs(pod *unstructured.Unstructured, containerName string, path string) error
}

type RealLogFetcher struct {
	k8s *k8sRetriever
}

func (r *RealLogFetcher) FetchLogs(pod *unstructured.Unstructured, containerName string, path string) error {
	return r.k8s.FetchLogs(pod, containerName, path)
}

type RealContainerProcessor struct {
	k8s *k8sRetriever
}

func (r *RealContainerProcessor) ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, saveFilesPath string) error {
	return r.k8s.ProcessContainer(pod, containerItem, saveFilesPath)
}

type RealPodDetailsExtractor struct {
	k8s *k8sRetriever
}

func (r *RealPodDetailsExtractor) extractPodDetails(pod *unstructured.Unstructured) (namespace string, podName string, nodeName string, podCreationTimestamp string, err error) {
	return r.k8s.extractPodDetails(pod)
}

type RealLogRequestCreator struct {
	k8s *k8sRetriever
}

func (r *RealLogRequestCreator) CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error) {
	return r.k8s.CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp)
}

type KubernetesConfigProvider interface {
	InClusterConfig() (*rest.Config, error)
}

type RealKubernetesConfigProvider struct{}

func (rkcp *RealKubernetesConfigProvider) InClusterConfig() (*rest.Config, error) {
	return rest.InClusterConfig()
}

type HandleKubernetesObjectsInterface interface {
	HandleKubernetesObjects(obj *unstructured.Unstructured, path string, objectName string) error
}

type RealHandleKubernetesObjects struct {
	k8s *k8sRetriever
}

func (r *RealHandleKubernetesObjects) HandleKubernetesObjects(obj *unstructured.Unstructured, path string, objectName string) error {
	return r.k8s.HandleKubernetesObjects(obj, path, objectName)

}

type k8sObjectParams struct {
	GVK          schema.GroupVersionKind
	ListOpts     []client.ListOption
	ResourceName string
	Namespace    string
}
