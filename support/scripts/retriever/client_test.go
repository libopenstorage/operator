package main

import (
	"fmt"
	"github.com/emicklei/go-restful/v3/log"
	"github.com/spf13/afero"
	"k8s.io/client-go/rest"
	"os"
	"path/filepath"
	"testing"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestInitializeRetriever(t *testing.T) {
	logger := logrus.New()

	// Initialize a fake filesystem
	fs := afero.NewOsFs()

	// Define a fake Kubeconfig content
	fakeKubeConfigContent := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://fake-cluster.local
  name: fake-cluster
contexts:
- context:
    cluster: fake-cluster
    user: fake-user
  name: fake-context
current-context: fake-context
users:
- name: fake-user
  user:
    token: fake-token
`
	// Define the fake Kubeconfig path
	err := os.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	if err != nil {
		return
	}

	kubeconfigPath = os.Getenv("KUBECONFIG")

	// Write the fake Kubeconfig content to the fake filesystem
	err = afero.WriteFile(fs, kubeconfigPath, []byte(fakeKubeConfigContent), 0644)
	if err != nil {
		t.Fatalf("Unable to write fake Kubeconfig: %v", err)
	}

	aferoFs := &afero.Afero{Fs: fs}

	// Now you can use aferoFs for file operations, and it will use the in-memory filesystem

	// Test cases
	tests := []struct {
		name          string
		initObjects   []runtime.Object
		expectError   bool
		errorContains string
	}{
		{
			name:        "Successfully initializes retriever",
			initObjects: []runtime.Object{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("KUBECONFIG", kubeconfigPath)

			log.Printf("KUBECONFIG content is: %s", os.Getenv("KUBECONFIG"))

			// Create a fake k8s client with the test objects
			fakeClient := testutil.FakeK8sClient(tt.initObjects...)

			// Call the function under test
			retriever, err := initializeRetriever(logger, fakeClient, aferoFs)

			// Assert that an error occurred or not based on the test case
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, retriever)
			}
		})
	}
}

func TestSetupLogging(t *testing.T) {
	// Setup a fake file system using afero
	fs := afero.NewOsFs()
	appFs := &afero.Afero{Fs: fs}

	// Temporary set the outputPath global variable to a fake path for testing
	// This should ideally be passed as a parameter to the function to avoid using a global variable
	oldOutputPath := outputPath
	defer func() { outputPath = oldOutputPath }()
	outputPath = "/tmp/test-logging"

	// Run the setupLogging function
	logger := setupLogging()
	logger.Info("Test log entry") // Add a test log entry

	// Assert a logger instance is returned
	assert.NotNil(t, logger, "Expected logger instance, got nil")

	// Assert that the directory was created
	dirExists, _ := appFs.DirExists(outputPath)
	assert.True(t, dirExists, "Log directory was not created")

	// Check permissions for the created directory
	dir, err := appFs.Stat(outputPath)
	assert.NoError(t, err, "Error while trying to stat log directory")
	if err == nil {
		expectedPermissions := "-rwxrw-rw-"
		actualPermissions := dir.Mode().Perm().String()
		assert.Equal(t, expectedPermissions, actualPermissions, "Directory permissions are not as expected")
	}

	// Assert that the log file was created
	logFilePath := filepath.Join(outputPath, "k8s-retriever.log")
	fileExists, _ := appFs.Exists(logFilePath)
	assert.True(t, fileExists, "Log file was not created")

}

type MockKubernetesConfigProvider struct {
	InClusterConfigFunc func() (*rest.Config, error)
}

func (m *MockKubernetesConfigProvider) InClusterConfig() (*rest.Config, error) {
	if m.InClusterConfigFunc != nil {
		return m.InClusterConfigFunc()
	}
	return &rest.Config{
		Host: "https://fake-cluster.local",
	}, nil
}

func TestSetK8sConfig(t *testing.T) {
	// Create a mock KubernetesConfigProvider
	mockProvider := &MockKubernetesConfigProvider{}

	// Initialize a fake filesystem
	fs := afero.NewOsFs()

	// Define a fake Kubeconfig content
	fakeKubeConfigContent := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://fake-cluster.local
  name: fake-cluster
contexts:
- context:
    cluster: fake-cluster
    user: fake-user
  name: fake-context
current-context: fake-context
users:
- name: fake-user
  user:
    token: fake-token
`
	// Define the fake Kubeconfig path
	err := os.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	if err != nil {
		return
	}

	kubeconfigPath = os.Getenv("KUBECONFIG")

	// Write the fake Kubeconfig content to the fake filesystem
	err = afero.WriteFile(fs, kubeconfigPath, []byte(fakeKubeConfigContent), 0644)
	if err != nil {
		t.Fatalf("Unable to write fake Kubeconfig: %v", err)
	}

	// Define the table of tests
	tests := []struct {
		name                  string
		kubeconfigPath        string
		kubernetesHost        string
		kubernetesPort        string
		expectedError         bool
		expectedErrorContains string
		mockProvider          KubernetesConfigProvider
	}{
		{
			name:           "In-cluster config with environment variables set",
			kubernetesHost: "fake-host",
			kubernetesPort: "443",
			expectedError:  false,
		},
		{
			name:                  "In-cluster config with missing environment variables",
			kubernetesHost:        "",
			kubernetesPort:        "",
			expectedError:         true,
			expectedErrorContains: "cannot determine running environment",
		},
		{
			name: "failed to get in-cluster config",
			mockProvider: &MockKubernetesConfigProvider{
				InClusterConfigFunc: func() (*rest.Config, error) {
					return nil, fmt.Errorf("failed to get in-cluster config")
				},
			},
			kubernetesHost:        "fake-host",
			kubernetesPort:        "443",
			expectedError:         true,
			expectedErrorContains: "failed to get in-cluster config",
		},
		{
			name:           "Out-of-cluster config with valid kubeconfig path",
			kubeconfigPath: "/tmp/kubeconfig",
			expectedError:  false,
		},
		{
			name:                  "Out-of-cluster config with invalid kubeconfig path",
			kubeconfigPath:        "invalid/path/to/kubeconfig",
			expectedError:         true,
			expectedErrorContains: "failed to get out-of-cluster config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// If a mock provider function is defined for a test, use it
			if tt.mockProvider != nil {
				mockProvider.InClusterConfigFunc = tt.mockProvider.InClusterConfig
			} else {
				// Reset to the default mock implementation if no specific behavior is defined for this test
				mockProvider.InClusterConfigFunc = func() (*rest.Config, error) {
					return &rest.Config{
						Host: "https://fake-cluster.local",
					}, nil
				}
			}

			// Set the kubeconfig path globally (if applicable)
			kubeconfigPath = tt.kubeconfigPath

			// Set environment variables for the test case
			os.Setenv("KUBERNETES_SERVICE_HOST", tt.kubernetesHost)
			os.Setenv("KUBERNETES_SERVICE_PORT", tt.kubernetesPort)
			defer func() {
				// Cleanup environment variables after each test case
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_PORT")
			}()

			// Prepare a k8sRetriever instance with a mock logger
			k8sRetrieverIns := &k8sRetriever{
				loggerToUse: logrus.New(),
			}

			// Call setK8sConfig with the mock config provider
			err := setK8sConfig(k8sRetrieverIns, mockProvider)

			// Assertions based on the expected outcome
			if tt.expectedError {
				assert.Error(t, err, "Expected an error but didn't get one")
				if tt.expectedErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorContains, "Error message does not contain expected text")
				}
			} else {
				assert.NoError(t, err, "Expected no error but got one")
				assert.NotNil(t, k8sRetrieverIns.restConfig, "restConfig should not be nil")
			}
		})
	}
}
