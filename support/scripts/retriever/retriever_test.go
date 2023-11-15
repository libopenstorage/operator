package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httptest"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"
)

func TestConvertToYaml(t *testing.T) {
	k8s := &k8sRetriever{}
	tests := []struct {
		name     string
		object   interface{}
		expected string
		hasError bool
		setUp    func()
	}{
		{
			name:     "Convert simple map",
			object:   map[string]string{"key": "value"},
			expected: "key: value\n",
			hasError: false,
			setUp: func() {
				k8s.yamlConverter = &RealYamlConverter{}
			},
		},
		{
			name: "Simulated error",
			object: func() interface{} {
				var i int
				return i
			},
			expected: "",
			hasError: true,
			setUp: func() {
				k8s.yamlConverter = &mockYamlConverter{err: errors.New("simulated error")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setUp != nil {
				tt.setUp() // call the setup function for the current test case
			}

			got, err := k8s.yamlConverter.ConvertToYaml(tt.object)
			if (err != nil) != tt.hasError {
				t.Errorf("convertToYaml() error = %v, wantErr %v", err, tt.hasError)
				return
			}
			if got != tt.expected {
				t.Errorf("convertToYaml() = %v, want %v", got, tt.expected)
			}
		})
	}

}

func TestWriteToFile(t *testing.T) {

	// create /tmp/test directory
	err := os.MkdirAll("/tmp/test", 0766)
	if err != nil {
		t.Errorf("Failed to create test directory: %v", err)
	}

	k8s := &k8sRetriever{
		writeToFileVar: &mockWriteToFile{},
		loggerToUse:    logrus.New(),
	}
	tests := []struct {
		name         string
		fileName     string
		resourceType string
		data         string
		basePath     string
		hasError     bool
		extension    string
		setup        func()
	}{
		{
			name:         "Write to file yaml",
			fileName:     "test",
			data:         "test data",
			resourceType: "test",
			basePath:     "/tmp/test",
			hasError:     false,
			extension:    "yaml",
			setup: func() {
				k8s.writeToFileVar = &RealWriteToFile{}
			},
		},
		{
			name:         "Write to file log",
			fileName:     "test",
			data:         "test data",
			resourceType: "test",
			basePath:     "/tmp/test",
			hasError:     false,
			extension:    "log",
			setup: func() {
				k8s.writeToFileVar = &RealWriteToFile{}
			},
		},
		{
			name:         "Simulated error, non-existent directory",
			fileName:     "",
			data:         "",
			resourceType: "test",
			basePath:     "/tmp/test/another",
			hasError:     true,
			setup: func() {
				k8s.writeToFileVar = &mockWriteToFile{err: errors.New("simulated error")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Call the setup function for the current test case if it's set
			if tt.setup != nil {
				tt.setup()
			}

			err := k8s.writeToFileVar.writeToFile(tt.fileName, tt.resourceType, tt.data, tt.basePath, tt.extension)
			if (err != nil) != tt.hasError {
				t.Errorf("writeToFile() error = %v, wantErr %v", err, tt.hasError)
				return
			}
		})
	}

	// Cleanup
	err = os.RemoveAll("/tmp/test")
	if err != nil {
		t.Errorf("Failed to remove test directory: %v", err)
	}
}

func TestConstructPath(t *testing.T) {
	tests := []struct {
		name       string
		subDir     string
		outputPath string
		expected   string
	}{
		{"No subdir", "", "/tmp", "/tmp/"},
		{"With subdir", "testdir", "/tmp", "/tmp/testdir"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{outputPath: tt.outputPath}
			got := k8s.constructPath(tt.subDir)
			if got != tt.expected {
				t.Errorf("constructPath() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestProcessK8sObjects(t *testing.T) {
	tests := []struct {
		name        string
		objectList  *unstructured.UnstructuredList
		shouldError bool
	}{
		{
			"Process single object",
			&unstructured.UnstructuredList{Items: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{"test": "data"},
				},
			}},
			false,
		},
		{
			"Process and fail",
			&unstructured.UnstructuredList{Items: []unstructured.Unstructured{
				{
					Object: map[string]interface{}{"fail": "data"},
				},
			}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				loggerToUse: logrus.New(),
			}
			err := k8s.processK8sObjects(tt.objectList, func(obj *unstructured.Unstructured) error {
				// Introduce a failure condition based on object data
				if val, exists := obj.Object["fail"]; exists && val == "data" {
					return errors.New("simulated processor error")
				}
				return nil
			})
			if (err != nil) != tt.shouldError {
				t.Errorf("processK8sObjects() error = %v, wantErr %v", err, tt.shouldError)
			}
		})
	}
}

func TestEnsureDirectory(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		shouldError bool
	}{
		{"Ensure /tmp/testdir", "/tmp/testdir", false},
		{"Ensure invalid directory", "/proc/testdir", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				fs:          afero.NewOsFs(),
				loggerToUse: logrus.New(),
			}
			err := k8s.ensureDirectory(tt.path)
			if (err != nil) != tt.shouldError {
				t.Errorf("ensureDirectory() error = %v, wantErr %v", err, tt.shouldError)
			}
		})
	}
}

func TestFetchK8sObjects(t *testing.T) {
	exampleGVK := schema.GroupVersionKind{
		Group:   "example.com",
		Version: "v1",
		Kind:    "ExampleKind",
	}

	exampleObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "example.com/v1",
			"kind":       "ExampleKind",
			"metadata": map[string]interface{}{
				"name":      "test-name",
				"namespace": "test-namespace",
			},
		},
	}

	tests := []struct {
		name               string
		initObjects        []runtime.Object
		gvk                schema.GroupVersionKind
		namespace          string
		listOptions        []client.ListOption
		expectedObjects    int
		shouldError        bool
		useMockErrorClient bool
	}{
		{
			name:            "Fetch no objects",
			initObjects:     nil,
			gvk:             exampleGVK,
			namespace:       "test-namespace",
			expectedObjects: 0,
			shouldError:     false,
		},
		{
			name:            "Fetch one object",
			initObjects:     []runtime.Object{exampleObject},
			gvk:             exampleGVK,
			namespace:       "test-namespace",
			expectedObjects: 1,
			shouldError:     false,
		},
		{
			name:            "Fetch one object with no namespace",
			initObjects:     []runtime.Object{exampleObject},
			gvk:             exampleGVK,
			namespace:       "",
			expectedObjects: 1,
			shouldError:     false,
		},
		{
			name:        "Fetch with applied list options",
			initObjects: []runtime.Object{exampleObject},
			gvk:         exampleGVK,
			namespace:   "test-namespace",
			listOptions: []client.ListOption{
				client.MatchingLabels(map[string]string{"key": "value"}),
			},
			expectedObjects: 0,
			shouldError:     false,
		},
		{
			name:        "List error",
			initObjects: []runtime.Object{},
			gvk:         exampleGVK,
			namespace:   "test-namespace",
			listOptions: []client.ListOption{
				client.MatchingLabels(map[string]string{"key": "value"}),
			},
			expectedObjects:    0,
			shouldError:        true,
			useMockErrorClient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var k8s *k8sRetriever

			if tt.useMockErrorClient {
				k8s = &k8sRetriever{
					k8sClient: &mockErrorClient{},
					context:   context.TODO(),
				}
			} else {
				k8s = &k8sRetriever{
					k8sClient: testutil.FakeK8sClient(tt.initObjects...),
					context:   context.TODO(),
				}
			}

			objList, err := k8s.fetchK8sObjects(tt.gvk, tt.namespace, tt.listOptions)
			if (err != nil) != tt.shouldError {
				t.Errorf("fetchK8sObjects() error = %v, wantErr %v", err, tt.shouldError)
			}

			if tt.shouldError && objList == nil {
				t.Logf("Expected error: %v", err)
				return

			}

			if len(objList.Items) != tt.expectedObjects {
				t.Errorf("Expected %d objects, but got %d", tt.expectedObjects, len(objList.Items))
			}
		})
	}

}

func TestListPods(t *testing.T) {
	examplePod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name": "test-pod",
			},
			"spec": map[string]interface{}{
				"nodeName": "test-node",
			},
		},
	}

	malformedPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
		},
	}

	tests := []struct {
		name          string
		initObjects   []runtime.Object
		namespace     string
		labelSelector string
		expectedError bool
		client        client.Client
		fs            FileSystem
		yamlConverter *mockYamlConverter
	}{
		{
			name:          "Successful retrieval and processing",
			initObjects:   []runtime.Object{examplePod},
			namespace:     "test-namespace",
			labelSelector: "app=example",
			client:        testutil.FakeK8sClient(),
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
		},
		{
			name:          "Error retrieving pods",
			initObjects:   []runtime.Object{},
			namespace:     "test-namespace",
			labelSelector: "app=example",
			expectedError: true,
			client:        &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: true},
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
		},
		{
			name:          "Error ensuring directory",
			initObjects:   []runtime.Object{examplePod},
			namespace:     "test-namespace",
			labelSelector: "app=example",
			expectedError: true,
			client:        testutil.FakeK8sClient(),
			fs:            &errorInjectingFS{Fs: afero.NewOsFs(), shouldError: true},
		},
		{
			name:          "Error converting label selector",
			initObjects:   []runtime.Object{examplePod},
			namespace:     "test-namespace",
			labelSelector: "===",
			expectedError: true,
			client:        testutil.FakeK8sClient(),
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
		},
		{
			name:          "Error processing pods and events",
			initObjects:   []runtime.Object{malformedPod},
			namespace:     "test-namespace",
			labelSelector: "app=example",
			expectedError: true,
			client: &errorInjectingClient{
				Client:          testutil.FakeK8sClient(),
				malformedObject: malformedPod,
			},
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter: &mockYamlConverter{err: errors.New("simulated error")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				k8sClient:     tt.client,
				context:       context.TODO(),
				fs:            tt.fs,
				scheme:        runtime.NewScheme(),
				loggerToUse:   &logrus.Logger{},
				yamlConverter: tt.yamlConverter,
			}

			err := k8s.listPods(tt.namespace, tt.labelSelector)
			if (err != nil) != tt.expectedError {
				t.Errorf("Expected error = %v, got %v", tt.expectedError, err)
			}
		})
	}
}

func TestProcessPodAndEvents(t *testing.T) {
	examplePod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name": "test-pod",
			},
			"spec": map[string]interface{}{
				"nodeName": "test-node",
			},
		},
	}

	emtpyNodeNamePod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name": "test-pod",
			},
			"spec": map[string]interface{}{},
		},
	}

	malformedPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
		},
	}

	specMissingPod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]interface{}{
				"name": "test-pod",
			},
		},
	}

	tests := []struct {
		name                string
		initObjects         []runtime.Object
		namespace           string
		labelSelector       string
		expectedError       bool
		client              client.Client
		fs                  FileSystem
		podToProcess        *unstructured.Unstructured
		expectedErrorMsg    string
		yamlConverter       *mockYamlConverter
		yamlEventsConverter *mockYamlConverter
		writeToFile         *mockWriteToFile
	}{
		{
			name:                "Successful retrieval and processing",
			initObjects:         []runtime.Object{examplePod},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			client:              testutil.FakeK8sClient(),
			podToProcess:        examplePod,
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: nil},
		},
		{
			name:                "Error retrieving pods",
			initObjects:         []runtime.Object{},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			expectedError:       true,
			podToProcess:        &unstructured.Unstructured{},
			client:              &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: true},
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: nil},
		}, {
			name:                "Error processing pods and events",
			initObjects:         []runtime.Object{malformedPod},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			expectedError:       true,
			podToProcess:        malformedPod,
			client:              &errorInjectingClient{Client: testutil.FakeK8sClient(), malformedObject: malformedPod, shouldError: false},
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: errors.New("simulated error")},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: nil},
		},
		{
			name:                "Empty node name in pod spec",
			initObjects:         []runtime.Object{emtpyNodeNamePod},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			expectedError:       true,
			podToProcess:        emtpyNodeNamePod,
			client:              testutil.FakeK8sClient(),
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: nil},
		}, {
			name:                "Error converting events to yaml",
			initObjects:         []runtime.Object{examplePod},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			expectedError:       true,
			podToProcess:        examplePod,
			client:              testutil.FakeK8sClient(),
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: errors.New("simulated error")},
			writeToFile:         &mockWriteToFile{err: nil},
		}, {
			name:                "Error writing pod yaml to file",
			initObjects:         []runtime.Object{examplePod},
			namespace:           "test-namespace",
			labelSelector:       "app=example",
			expectedError:       true,
			podToProcess:        examplePod,
			client:              testutil.FakeK8sClient(),
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: true},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: errors.New("simulated error")},
		}, {
			name:                "Spec missing in pod",
			initObjects:         []runtime.Object{specMissingPod},
			namespace:           "test-namespace",
			expectedError:       true,
			podToProcess:        specMissingPod,
			client:              testutil.FakeK8sClient(),
			fs:                  &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:       &mockYamlConverter{err: nil},
			yamlEventsConverter: &mockYamlConverter{err: nil},
			writeToFile:         &mockWriteToFile{err: nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{

				k8sClient:           tt.client,
				context:             context.TODO(),
				fs:                  tt.fs,
				scheme:              runtime.NewScheme(),
				loggerToUse:         &logrus.Logger{},
				yamlConverter:       tt.yamlConverter,
				yamlEventsConverter: tt.yamlEventsConverter,
				writeToFileVar:      tt.writeToFile,
			}

			err := k8s.processPodAndEvents(tt.podToProcess, "/tmp")

			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}

		})
	}

	// Cleanup
	err := os.RemoveAll("/tmp/pod_test-node-test-pod.yaml")
	if err != nil {
		t.Errorf("Failed to remove test file: %v", err)
	}

}

func TestGetLogsForPod(t *testing.T) {
	outputPath := "/tmp"

	tests := []struct {
		name          string
		namespace     string
		labelSelector string
		expectedError bool
		client        client.Client
		fs            FileSystem
		outputPath    string
	}{
		{
			name:          "Successful retrieval and processing",
			namespace:     "test-namespace",
			labelSelector: "app=example",
			client:        testutil.FakeK8sClient(),
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			outputPath:    outputPath,
		},
		{
			name:          "Error converting label selector",
			namespace:     "test-namespace",
			labelSelector: "===",
			expectedError: true,
			client:        testutil.FakeK8sClient(),
			fs:            &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			outputPath:    outputPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				k8sClient:   tt.client,
				context:     context.TODO(),
				fs:          tt.fs,
				outputPath:  tt.outputPath,
				loggerToUse: &logrus.Logger{},
			}

			err := k8s.getLogsForPod(tt.namespace, tt.labelSelector)
			if (err != nil) != tt.expectedError {
				t.Errorf("Expected error = %v, got %v", tt.expectedError, err)
			}
		})
	}
}

func TestProcessPodForLogs(t *testing.T) {
	saveFilesPath := "/tmp/logs"
	validContainer := map[string]interface{}{
		"name":  "valid-container",
		"image": "valid-image",
	}

	tests := []struct {
		name          string
		pod           *unstructured.Unstructured
		expectedCalls int
	}{
		{
			name: "Successfully process containers in a pod",
			pod: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": []interface{}{validContainer},
					},
				},
			},
			expectedCalls: 1,
		},
		{
			name: "No spec in pod",
			pod: &unstructured.Unstructured{
				Object: map[string]interface{}{},
			},
			expectedCalls: 0,
		},
		{
			name: "No containers in pod spec",
			pod: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{},
				},
			},
			expectedCalls: 0,
		},
		{
			name: "Malformed containers in pod spec",
			pod: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": "invalid",
					},
				},
			},
			expectedCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProcessor := &mockContainerProcessor{}
			k8s := &k8sRetriever{
				loggerToUse:        &logrus.Logger{},
				containerProcessor: mockProcessor,
			}

			err := k8s.processPodForLogs(tt.pod, saveFilesPath)
			if err != nil {
				return
			}
			if mockProcessor.callCount != tt.expectedCalls {
				t.Errorf("Expected %d calls to ProcessContainer, got %d", tt.expectedCalls, mockProcessor.callCount)
			}
		})
	}
}

func TestProcessContainer(t *testing.T) {
	saveFilesPath := "/tmp/logs"
	validContainer := map[string]interface{}{
		"name":  "valid-container",
		"image": "valid-image",
	}

	tests := []struct {
		name             string
		container        interface{}
		expectedLog      string
		expectedLogError string
		getLogsError     error
	}{
		{
			name:             "Successfully process valid container",
			container:        validContainer,
			expectedLog:      "Fetching logs for Container: valid-container",
			expectedLogError: "",
			getLogsError:     nil,
		},
		{
			name:             "Fail to process malformed container",
			container:        "invalid",
			expectedLog:      "",
			expectedLogError: "",
			getLogsError:     nil,
		},
		{
			name: "Fail to fetch logs for valid container",
			container: map[string]interface{}{
				"name": "error-container",
			},
			expectedLog:      "Fetching logs for Container: error-container",
			expectedLogError: "error fetching logs for container error-container in pod test-pod: some error",
			getLogsError:     errors.New("some error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logBuffer := new(bytes.Buffer)
			logger := logrus.New()
			logger.Out = logBuffer

			k8s := &k8sRetriever{
				loggerToUse: logger,
				logFetcher:  &mockLogFetcher{err: tt.getLogsError},
			}

			pod := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod",
					},
				},
			}

			err := k8s.ProcessContainer(pod, tt.container, saveFilesPath)
			if err != nil {
				return
			}

			if !strings.Contains(logBuffer.String(), tt.expectedLog) {
				t.Errorf("Expected log \"%s\" not found in log output", tt.expectedLog)
			}

			if !strings.Contains(logBuffer.String(), tt.expectedLogError) {
				t.Errorf("Expected error log \"%s\" not found in log output", tt.expectedLogError)
			}
		})
	}
}

func TestFetchLogs(t *testing.T) {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail-log-request" {
			http.Error(w, "forced failure", http.StatusInternalServerError) // Simulate server error for log request creation
			return
		}
		if r.URL.Path == "/fail-do-raw" {
			// To simulate an error in DoRaw, we can abruptly close the connection
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte("mocked logs")) // Default case, returns mocked logs
	}))
	defer server.Close()

	// Modify the restConfig to use the mock server URL
	restConfig := &rest.Config{
		Host: server.URL,
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
			GroupVersion:         &schema.GroupVersion{Group: "", Version: "v1"},
		},
	}

	logBuffer := new(bytes.Buffer)
	logger := logrus.New()
	logger.Out = logBuffer

	mockPodDetails := &mockPodDetailsExtractor{}
	mockFileWriter := &mockWriteToFile{}
	mockRestRequester := &mockRestRequest{}
	mockLogRequest := &mockLogRequestCreator{
		request: mockRestRequester,
	}

	k8s := &k8sRetriever{
		loggerToUse:         logger,
		podDetailsExtractor: mockPodDetails,
		writeToFileVar:      mockFileWriter,
		restConfig:          restConfig,
		context:             context.TODO(),

		logRequestCreator: &RealLogRequestCreator{
			k8s: &k8sRetriever{
				restConfig:  restConfig,
				loggerToUse: logger,
			},
		},
	}

	containerName := "test-container"

	pod := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":      "test-pod", // Pod name must not be empty
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{
						"name": containerName,
					},
				},
			},
		},
	}

	saveFilesPath := "/tmp"

	tests := []struct {
		name             string
		podDetailsErr    error
		requestErr       error
		writeFileErr     error
		doRawErr         error
		wantErr          bool
		modifyServerPath func()
	}{
		{
			name:          "successful log retrieval",
			podDetailsErr: nil,
			requestErr:    nil,
			writeFileErr:  nil,
			wantErr:       false,
		},
		{
			name:          "error in pod details extraction",
			podDetailsErr: errors.New("mocked pod details extraction error"),
			wantErr:       true,
		},
		{
			name:          "error in log request creation",
			podDetailsErr: errors.New("mocked pod details extraction error"),
			requestErr:    errors.New("mocked log request creation error"),
			wantErr:       true,
			modifyServerPath: func() {
				restConfig.Host = server.URL + "/fail-log-request" // Trigger failure in log request creation
			},
		},
		{
			name:          "error in writing to file",
			podDetailsErr: nil,
			requestErr:    nil,
			writeFileErr:  errors.New("mocked write to file error"),
			wantErr:       true,
		},
		{
			name:          "error reading logs with DoRaw",
			podDetailsErr: errors.New("mocked log request creation error"),
			requestErr:    errors.New("mocked log request creation error"),
			writeFileErr:  nil,
			doRawErr:      errors.New("mocked log request creation error"),
			wantErr:       true,
			modifyServerPath: func() {
				restConfig.Host = server.URL + "/fail-do-raw" // Trigger failure in DoRaw
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			// Set up the mocks based on the test case
			mockPodDetails.err = tt.podDetailsErr
			mockLogRequest.err = tt.requestErr
			mockFileWriter.err = tt.writeFileErr
			mockRestRequester.err = tt.doRawErr
			if tt.modifyServerPath != nil {
				tt.modifyServerPath()
			}
			err := k8s.FetchLogs(pod, containerName, saveFilesPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("FetchLogs() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractPodDetails(t *testing.T) {
	k8s := &k8sRetriever{
		loggerToUse: &logrus.Logger{},
	}

	tests := []struct {
		name                  string
		podObject             map[string]interface{}
		expectedNamespace     string
		expectedPodName       string
		expectedNodeName      string
		expectedCreationTime  string
		expectedErrorContains string
	}{
		{
			name: "successful extraction",
			podObject: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":              "test-pod",
					"namespace":         "test-namespace",
					"creationTimestamp": "2023-10-31T10:00:00Z",
				},
				"spec": map[string]interface{}{
					"nodeName": "test-node",
				},
			},
			expectedNamespace:    "test-namespace",
			expectedPodName:      "test-pod",
			expectedNodeName:     "test-node",
			expectedCreationTime: "2023-10-31T10:00:00Z",
		},
		{
			name: "failure to extract namespace",
			podObject: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-pod",
				},
			},
			expectedErrorContains: "failed to extract namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &unstructured.Unstructured{Object: tt.podObject}

			namespace, podName, nodeName, creationTime, err := k8s.extractPodDetails(pod)

			if err != nil && tt.expectedErrorContains != "" {
				if !strings.Contains(err.Error(), tt.expectedErrorContains) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.expectedErrorContains, err.Error())
				}
				return
			} else if err != nil {
				t.Fatalf("Unexpected error: %s", err.Error())
			}

			if namespace != tt.expectedNamespace {
				t.Errorf("Expected namespace '%s', got '%s'", tt.expectedNamespace, namespace)
			}

			if podName != tt.expectedPodName {
				t.Errorf("Expected pod name '%s', got '%s'", tt.expectedPodName, podName)
			}

			if nodeName != tt.expectedNodeName {
				t.Errorf("Expected node name '%s', got '%s'", tt.expectedNodeName, nodeName)
			}

			if creationTime != tt.expectedCreationTime {
				t.Errorf("Expected creation time '%s', got '%s'", tt.expectedCreationTime, creationTime)
			}
		})
	}
}

func TestCreateLogsRequest(t *testing.T) {
	tests := []struct {
		name                 string
		namespace            string
		podName              string
		containerName        string
		podCreationTimestamp string
		expectedPath         string
		expectedQuery        string
	}{
		{
			name:                 "Without timestamp",
			namespace:            "test-namespace",
			podName:              "test-pod",
			containerName:        "test-container",
			podCreationTimestamp: "",
			expectedPath:         "/api/v1/namespaces/test-namespace/pods/test-pod/log",
			expectedQuery:        "container=test-container",
		},
		{
			name:                 "With timestamp",
			namespace:            "test-namespace",
			podName:              "test-pod",
			containerName:        "test-container",
			podCreationTimestamp: "2023-01-01T00:00:00Z",
			expectedPath:         "/api/v1/namespaces/test-namespace/pods/test-pod/log",
			expectedQuery:        "container=test-container&sinceTime=2023-01-01T00%3A00%3A00Z",
		},
		{
			name:                 "Invalid request",
			namespace:            "test-namespace",
			podName:              "test-pod",
			containerName:        "test-container",
			podCreationTimestamp: "invalid",
			expectedPath:         "/api/v1/namespaces/test-namespace/pods/test-pod/log",
			expectedQuery:        "container=test-container&sinceTime=invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				restConfig:  &rest.Config{},
				loggerToUse: &logrus.Logger{},
			}

			t.Logf("handleDeploymentFunc set: %v", k8s.handleDeploymentFunc)

			req, _ := k8s.CreateLogsRequest(tt.namespace, tt.podName, tt.containerName, tt.podCreationTimestamp)
			if req.URL() == nil {
				t.Errorf("expected URL to be set")
			}

			if req.URL().Path != tt.expectedPath {
				t.Errorf("expected path to be %s, got %s", tt.expectedPath, req.URL().Path)
			}

			if req.URL().RawQuery != tt.expectedQuery {
				t.Errorf("expected query to be %s, got %s", tt.expectedQuery, req.URL().RawQuery)
			}

		})
	}
}

func TestListDeployments(t *testing.T) {
	// Mock deployments
	exampleDeployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name": "test-deployment",
			},
		},
	}

	tests := []struct {
		name             string
		initObjects      []runtime.Object
		namespace        string
		expectedError    bool
		client           client.Client
		fs               FileSystem
		yamlConverter    *mockYamlConverter
		writeToFile      *mockWriteToFile
		handleDeployment *mockHandleDeployment
		expectedErrorMsg string
		deployToProcess  *unstructured.Unstructured
	}{
		{
			name:             "Successful retrieval and processing",
			initObjects:      []runtime.Object{exampleDeployment},
			namespace:        "test-namespace",
			client:           testutil.FakeK8sClient(),
			fs:               &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			expectedErrorMsg: "",
			handleDeployment: &mockHandleDeployment{},
			deployToProcess:  exampleDeployment,
		},
		{
			name:             "Error retrieving deployments",
			initObjects:      []runtime.Object{},
			namespace:        "test-namespace",
			expectedError:    true,
			client:           &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: true},
			fs:               &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			handleDeployment: &mockHandleDeployment{},
			deployToProcess:  exampleDeployment,
		},
		{
			name:             "Error processing deployments",
			initObjects:      []runtime.Object{exampleDeployment},
			namespace:        "test-namespace",
			expectedError:    true,
			client:           &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: exampleDeployment},
			fs:               &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			handleDeployment: &mockHandleDeployment{errors.New("simulated error")},
			deployToProcess:  exampleDeployment,
		},
		{
			name:             "Error on ensure directory",
			initObjects:      []runtime.Object{exampleDeployment},
			namespace:        "test-namespace",
			expectedError:    true,
			client:           &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: exampleDeployment},
			fs:               &errorInjectingFS{Fs: afero.NewOsFs(), shouldError: true},
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			handleDeployment: &mockHandleDeployment{},
			deployToProcess:  exampleDeployment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				k8sClient:            tt.client,
				context:              context.TODO(),
				fs:                   tt.fs,
				scheme:               runtime.NewScheme(),
				loggerToUse:          &logrus.Logger{},
				yamlConverter:        tt.yamlConverter,
				writeToFileVar:       tt.writeToFile,
				handleDeploymentFunc: tt.handleDeployment,
			}

			t.Logf("Example Deployment: %v", exampleDeployment)

			err := k8s.listDeployments(tt.namespace)
			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}

		})
	}
}

func TestHandleDeployment(t *testing.T) {
	exampleDeployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name": "test-deployment",
			},
			"spec": map[string]interface{}{
				"nodeName": "test-node",
			},
		},
	}

	brokenDeployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name": "test-deployment",
			},
		},
	}

	tests := []struct {
		name             string
		object           *unstructured.Unstructured
		saveFilesPath    string
		yamlConverter    *mockYamlConverter
		writeToFile      *mockWriteToFile
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name:          "Successful processing of deployment",
			object:        exampleDeployment,
			saveFilesPath: "/tmp",
			yamlConverter: &mockYamlConverter{err: nil},
			writeToFile:   &mockWriteToFile{err: nil},
		},
		{
			name:             "Error converting to YAML",
			object:           exampleDeployment,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: errors.New("YAML conversion error")},
			writeToFile:      &mockWriteToFile{err: nil},
			expectedError:    true,
			expectedErrorMsg: "YAML conversion error",
		},
		{
			name:             "Error writing to file",
			object:           exampleDeployment,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: errors.New("file write error")},
			expectedError:    true,
			expectedErrorMsg: "file write error",
		},
		{
			name:             "Error when no spec in deployment",
			object:           brokenDeployment,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			expectedError:    true,
			expectedErrorMsg: "deployment test-deployment does not have a spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				loggerToUse:    &logrus.Logger{},
				yamlConverter:  tt.yamlConverter,
				writeToFileVar: tt.writeToFile,
			}

			err := k8s.HandleDeployment(tt.object, tt.saveFilesPath)

			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}
		})
	}
}

func TestListStorageClusters(t *testing.T) {
	// Mock storage clusters
	exampleStorageCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.libopenstorage.org/v1",
			"kind":       "StorageCluster",
			"metadata": map[string]interface{}{
				"name": "test-storagecluster",
			},
			"spec": map[string]interface{}{
				"nodeName": "test-node",
			},
		},
	}

	wrongStorageCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.libopenstorage.org/v1",
			"kind":       "StorageCluster",
			"metadata": map[string]interface{}{
				"name": "test-storagecluster",
			},
		},
	}

	tests := []struct {
		name                    string
		initObjects             []runtime.Object
		namespace               string
		expectedError           bool
		client                  client.Client
		fs                      FileSystem
		yamlConverter           *mockYamlConverter
		writeToFile             *mockWriteToFile
		handleStorageCluster    *mockHandleStorageCluster
		expectedErrorMsg        string
		StorageClusterToProcess *unstructured.Unstructured
	}{
		{
			name:                    "Successful retrieval and processing",
			initObjects:             []runtime.Object{exampleStorageCluster},
			namespace:               "test-namespace",
			client:                  testutil.FakeK8sClient(),
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			expectedErrorMsg:        "",
			handleStorageCluster:    &mockHandleStorageCluster{},
			StorageClusterToProcess: exampleStorageCluster,
		},
		{
			name:                    "Error retrieving storage clusters",
			initObjects:             []runtime.Object{},
			namespace:               "test-namespace",
			expectedError:           true,
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: true},
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleStorageCluster:    &mockHandleStorageCluster{},
			StorageClusterToProcess: exampleStorageCluster,
		},
		{
			name:                    "Error processing storage clusters",
			initObjects:             []runtime.Object{wrongStorageCluster},
			namespace:               "test-namespace",
			expectedError:           true,
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: wrongStorageCluster},
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleStorageCluster:    &mockHandleStorageCluster{errors.New("simulated error")},
			StorageClusterToProcess: wrongStorageCluster,
		}, {
			name:                    "Error on ensure directory",
			initObjects:             []runtime.Object{exampleStorageCluster},
			namespace:               "test-namespace",
			expectedError:           true,
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: exampleStorageCluster},
			fs:                      &errorInjectingFS{Fs: afero.NewOsFs(), shouldError: true},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleStorageCluster:    &mockHandleStorageCluster{},
			StorageClusterToProcess: exampleStorageCluster,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				k8sClient:                tt.client,
				context:                  context.TODO(),
				fs:                       tt.fs,
				scheme:                   runtime.NewScheme(),
				loggerToUse:              &logrus.Logger{},
				yamlConverter:            tt.yamlConverter,
				writeToFileVar:           tt.writeToFile,
				handleStorageClusterFunc: tt.handleStorageCluster,
			}

			t.Logf("handleStorageCluster set: %v", tt.handleStorageCluster.HandleStorageCluster(exampleStorageCluster, "/tmp"))

			t.Logf("Example Storage Cluster: %v", exampleStorageCluster)

			err := k8s.listStorageClusters(tt.namespace)
			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}

		})
	}
}

func TestHandleStorageCluster(t *testing.T) {
	exampleStorageCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.libopenstorage.org/v1",
			"kind":       "StorageCluster",
			"metadata": map[string]interface{}{
				"name": "test-storagecluster",
			},
			"spec": map[string]interface{}{
				"nodeName": "test-node",
			},
		},
	}

	brokenStorageCluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "core.libopenstorage.org/v1",
			"kind":       "StorageCluster",
			"metadata": map[string]interface{}{
				"name": "test-storagecluster",
			},
		},
	}

	tests := []struct {
		name             string
		object           *unstructured.Unstructured
		saveFilesPath    string
		yamlConverter    *mockYamlConverter
		writeToFile      *mockWriteToFile
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name:          "Successful processing of deployment",
			object:        exampleStorageCluster,
			saveFilesPath: "/tmp",
			yamlConverter: &mockYamlConverter{err: nil},
			writeToFile:   &mockWriteToFile{err: nil},
		},
		{
			name:             "Error converting to YAML",
			object:           exampleStorageCluster,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: errors.New("YAML conversion error")},
			writeToFile:      &mockWriteToFile{err: nil},
			expectedError:    true,
			expectedErrorMsg: "YAML conversion error",
		},
		{
			name:             "Error writing to file",
			object:           exampleStorageCluster,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: errors.New("file write error")},
			expectedError:    true,
			expectedErrorMsg: "file write error",
		},
		{
			name:             "Error when no spec in deployment",
			object:           brokenStorageCluster,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: nil},
			expectedError:    true,
			expectedErrorMsg: "storage cluster test-storagecluster does not have a spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				loggerToUse:    &logrus.Logger{},
				yamlConverter:  tt.yamlConverter,
				writeToFileVar: tt.writeToFile,
			}

			err := k8s.HandleStorageCluster(tt.object, tt.saveFilesPath)

			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}
		})
	}
}

func TestListKubernetesObjects(t *testing.T) {
	exampleObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "group/v1",
			"kind":       "ExampleKind",
			"metadata": map[string]interface{}{
				"name": "test-object",
			},
			"spec": map[string]interface{}{
				"exampleField": "exampleValue",
			},
		},
	}

	wrongObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "group/v1",
			"kind":       "ExampleKind",
			"metadata": map[string]interface{}{
				"name": "test-object",
			},
			// No "spec" field, will simulate an error unless the object is an exception
		},
	}

	storageClassObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "storage.k8s.io/v1",
			"kind":       "StorageClass",
			"metadata": map[string]interface{}{
				"name": "test-storageclass",
			},
		},
	}

	volumeSnapshotClassObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "snapshot.storage.k8s.io/v1",
			"kind":       "VolumeSnapshotClass",
			"metadata": map[string]interface{}{
				"name": "test-volumesnapshotclass",
			},
		},
	}

	volumePlacementStrategyObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "portworx.io/v1beta2",
			"kind":       "VolumePlacementStrategy",
			"metadata": map[string]interface{}{
				"name": "test-volumeplacementstrategy",
			},
		},
	}

	tests := []struct {
		name                    string
		namespace               string
		gvk                     schema.GroupVersionKind
		opts                    []client.ListOption
		objectName              string
		initObjects             []runtime.Object
		expectedError           bool
		expectedErrorMsg        string
		client                  client.Client
		fs                      FileSystem
		yamlConverter           *mockYamlConverter
		writeToFile             *mockWriteToFile
		handleKubernetesObjects *mockHandleKubernetesObjects
		outputPath              string
	}{
		{
			name:                    "Successful retrieval and processing",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "ExampleKindList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "examplekind",
			initObjects:             []runtime.Object{exampleObject},
			client:                  testutil.FakeK8sClient(),
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/tmp",
		},
		{
			name:                    "Successful retrieval and processing with storage class",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "storage.k8s.io", Version: "v1", Kind: "StorageClassList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "storageclass",
			initObjects:             []runtime.Object{storageClassObject},
			client:                  testutil.FakeK8sClient(),
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/tmp",
		},
		{
			name:                    "Successful retrieval and processing with volume snapshot class",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1beta1", Kind: "VolumeSnapshotClassList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "volumesnapshotclass",
			initObjects:             []runtime.Object{volumeSnapshotClassObject},
			client:                  testutil.FakeK8sClient(),
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/tmp",
		},
		{
			name:                    "Successful retrieval and processing with volume placement strategy",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "portworx.io", Version: "v1beta2", Kind: "VolumePlacementStrategyList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "vps",
			initObjects:             []runtime.Object{volumePlacementStrategyObject},
			client:                  testutil.FakeK8sClient(),
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/tmp",
		},
		{
			name:                    "Error retrieving objects",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "ExampleKindList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "examplekind",
			initObjects:             []runtime.Object{},
			expectedError:           true,
			expectedErrorMsg:        "error retrieving examplekinds:",
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: true},
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/tmp",
		},
		{
			name:                    "Error processing objects",
			namespace:               "test-namespace",
			gvk:                     schema.GroupVersionKind{Group: "group", Version: "v1", Kind: "ExampleKindList"},
			opts:                    []client.ListOption{client.InNamespace("test-namespace")},
			objectName:              "examplekind",
			initObjects:             []runtime.Object{wrongObject},
			expectedError:           true,
			expectedErrorMsg:        "simulated error",
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: wrongObject},
			fs:                      &errorInjectingFS{Fs: afero.NewMemMapFs(), shouldError: false},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{errors.New("simulated error")},
			outputPath:              "/tmp",
		},
		{
			name:                    "Error on ensure directory",
			initObjects:             []runtime.Object{exampleObject},
			namespace:               "test-namespace",
			expectedError:           true,
			client:                  &errorInjectingClient{Client: testutil.FakeK8sClient(), shouldError: false, malformedObject: exampleObject},
			fs:                      &errorInjectingFS{Fs: afero.NewOsFs(), shouldError: true},
			yamlConverter:           &mockYamlConverter{err: nil},
			writeToFile:             &mockWriteToFile{err: nil},
			handleKubernetesObjects: &mockHandleKubernetesObjects{},
			outputPath:              "/proc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				k8sClient:                   tt.client,
				context:                     context.TODO(),
				fs:                          tt.fs,
				scheme:                      runtime.NewScheme(),
				loggerToUse:                 &logrus.Logger{},
				yamlConverter:               tt.yamlConverter,
				writeToFileVar:              tt.writeToFile,
				handleKubernetesObjectsFunc: tt.handleKubernetesObjects,
				outputPath:                  tt.outputPath,
			}

			err := k8s.listKubernetesObjects(tt.namespace, tt.gvk, tt.opts, tt.objectName)
			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}
		})
	}
}

func TestHandleKubernetesObjects(t *testing.T) {
	exampleObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "group/v1",
			"kind":       "ExampleKind",
			"metadata": map[string]interface{}{
				"name": "test-object",
			},
			"spec": map[string]interface{}{
				"exampleField": "exampleValue",
			},
		},
	}

	brokenObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "group/v1",
			"kind":       "ExampleKind",
			"metadata": map[string]interface{}{
				"name": "test-object",
			},
		},
	}

	tests := []struct {
		name             string
		object           *unstructured.Unstructured
		saveFilesPath    string
		objectName       string
		yamlConverter    *mockYamlConverter
		writeToFile      *mockWriteToFile
		expectedError    bool
		expectedErrorMsg string
	}{
		{
			name:          "Successful processing of object",
			object:        exampleObject,
			saveFilesPath: "/tmp",
			objectName:    "examplekind",
			yamlConverter: &mockYamlConverter{err: nil},
			writeToFile:   &mockWriteToFile{err: nil},
		},
		{
			name:          "Error converting to YAML",
			object:        exampleObject,
			saveFilesPath: "/tmp",
			objectName:    "examplekind",
			yamlConverter: &mockYamlConverter{err: errors.New("YAML conversion error")},
			writeToFile:   &mockWriteToFile{err: nil},
			expectedError: true,
		},
		{
			name:          "broken object",
			object:        brokenObject,
			saveFilesPath: "/tmp",
			objectName:    "examplekind",
			yamlConverter: &mockYamlConverter{err: nil},
			writeToFile:   &mockWriteToFile{err: nil},
			expectedError: true,
		},
		{
			name:             "Error writing to file",
			object:           exampleObject,
			saveFilesPath:    "/tmp",
			yamlConverter:    &mockYamlConverter{err: nil},
			writeToFile:      &mockWriteToFile{err: errors.New("file write error")},
			expectedError:    true,
			expectedErrorMsg: "file write error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8s := &k8sRetriever{
				loggerToUse:    &logrus.Logger{},
				yamlConverter:  tt.yamlConverter,
				writeToFileVar: tt.writeToFile,
			}

			err := k8s.HandleKubernetesObjects(tt.object, tt.saveFilesPath, tt.objectName)

			if tt.expectedError {
				if err == nil {
					t.Fatalf("Expected error, got nil")
				} else if tt.expectedErrorMsg != "" && !strings.Contains(err.Error(), tt.expectedErrorMsg) {
					t.Fatalf("Expected error message to contain '%s', got '%s'", tt.expectedErrorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, got %s", err)
				}
			}
		})
	}
}

func TestRetrieveMultipathConf(t *testing.T) {
	appFS := afero.NewOsFs()
	logger := logrus.New()

	tests := []struct {
		name              string
		fileExists        bool
		createDirError    bool
		writeFileError    bool
		expectError       bool
		expectedLogOutput string
		saveFilesPath     string
	}{
		{
			name:              "File exists and successfully retrieved",
			fileExists:        true,
			expectError:       false,
			expectedLogOutput: "Successfully saved multipath config to",
			saveFilesPath:     "/tmp",
		},
		{
			name:              "File does not exist",
			fileExists:        false,
			expectError:       false,
			expectedLogOutput: "File is not present /etc/multipath.conf",
			saveFilesPath:     "/tmp",
		},
		{
			name:              "Error saving file",
			fileExists:        true,
			writeFileError:    true,
			expectError:       true,
			expectedLogOutput: "Error saving multipath config to",
			saveFilesPath:     "/proc",
		},
		{
			name:              "Error reading file",
			fileExists:        true,
			createDirError:    true,
			expectError:       true,
			expectedLogOutput: "Error reading multipath config from",
			saveFilesPath:     "/tmp",
		},
		{
			name:              "Error creating directory",
			fileExists:        true,
			createDirError:    true,
			expectError:       true,
			expectedLogOutput: "Error creating directory",
			saveFilesPath:     "/proc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			k8s := &k8sRetriever{
				fs:          appFS,
				loggerToUse: logger,
			}

			if tc.fileExists {
				err := afero.WriteFile(appFS, "/etc/multipath.conf", []byte("test content"), 0644)
				if err != nil {
					return
				}
			}

			if tc.createDirError {
				appFS = afero.NewReadOnlyFs(appFS)
			}

			// Execute
			err := k8s.retrieveMultipathConf("testNode")

			// Assertions
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

		})
	}
}
