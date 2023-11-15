package main

import (
	"context"
	"fmt"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"log"
	"net/url"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mockErrorClient struct {
	client.Client
}

type errorInjectingClient struct {
	client.Client
	shouldError     bool
	malformedObject *unstructured.Unstructured
}

type FileSystem interface {
	afero.Fs
	EnsureDirectory(path string) error
}

type errorInjectingFS struct {
	afero.Fs
	shouldError bool
}

type mockContainerProcessor struct {
	callCount int
	err       error
}

func (m *mockContainerProcessor) ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, path string) error {
	m.callCount++
	return m.err
}

type mockLogFetcher struct {
	err error
}

type mockYamlConverter struct {
	err error
}

func (m *mockYamlConverter) ConvertToYaml(object interface{}) (string, error) {
	return "", m.err

}

type mockWriteToFile struct {
	err error
}

func (m *mockWriteToFile) writeToFile(resourceName string, resourceType string, dataToProcess string, basePath string, fileExtensions ...string) error {
	return m.err
}

func (m *mockLogFetcher) FetchLogs(pod *unstructured.Unstructured, containerName string, path string) error {
	return m.err
}

func (m *mockErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return fmt.Errorf("mocked error")
}

func (e *errorInjectingFS) EnsureDirectory(path string) error {
	if e.shouldError {
		return fmt.Errorf("injected fs error")
	}
	return e.Fs.MkdirAll(path, 0755)
}

func (e *errorInjectingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if e.shouldError {
		return fmt.Errorf("injected error")
	}

	// Inject the malformed object if it is set
	if e.malformedObject != nil {
		podList, ok := list.(*unstructured.UnstructuredList)
		if ok {
			podList.Items = append(podList.Items, *e.malformedObject)
			return nil
		}
	}

	return e.Client.List(ctx, list, opts...)
}

type mockPodDetailsExtractor struct {
	err error
}

func (m *mockPodDetailsExtractor) extractPodDetails(pod *unstructured.Unstructured) (namespace, podName, nodeName, podCreationTimestamp string, err error) {
	if m.err != nil {
		return "", "", "", "", m.err
	}
	return "default", "test-pod", "test-node", "", nil // Return valid details here
}

type mockLogRequestCreator struct {
	request *mockRestRequest
	err     error
}

type mockRestRequest struct {
	err error
}

func (m *mockRestRequest) DoRaw(ctx context.Context) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []byte("mocked logs"), nil
}

func (m *mockLogRequestCreator) CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &rest.Request{}, nil

}

type MockRESTClient struct{}

func (m *MockRESTClient) Get() *rest.Request {
	return rest.NewRequestWithClient(&url.URL{}, "", rest.ClientContentConfig{}, nil)
}

type mockHandleDeployment struct {
	err error
}

func (m *mockHandleDeployment) HandleDeployment(obj *unstructured.Unstructured, path string) error {

	log.Printf("mockHandleDeployment.HandleDeployment called")

	return m.err
}

type mockHandleStorageCluster struct {
	err error
}

func (m *mockHandleStorageCluster) HandleStorageCluster(obj *unstructured.Unstructured, path string) error {

	log.Printf("mockHandleStorageCluster.HandleStorageCluster called")

	return m.err
}

type mockHandleKubernetesObjects struct {
	err error
}

func (m *mockHandleKubernetesObjects) HandleKubernetesObjects(obj *unstructured.Unstructured, path string, objectName string) error {

	log.Printf("mockHandleKubernetesObjects.HandleKubernetesObjects called")

	return m.err
}
