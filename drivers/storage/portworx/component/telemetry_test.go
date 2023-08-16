package component

import (
	"testing"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockPxUtil struct {
	mock.Mock
}

func (m *MockPxUtil) GetPortworxVersion(cluster *corev1.StorageCluster) *version.Version {
	args := m.Called(cluster)
	return args.Get(0).(*version.Version)
}

func (m *MockPxUtil) StartPort(cluster *corev1.StorageCluster) int {
	args := m.Called(cluster)
	return args.Int(0)
}

func TestGetCCMListeningPort(t *testing.T) {
	// Create a mock pxutil instance
	mockPxUtil := new(MockPxUtil)

	// Set up expectations for GetPortworxVersion method
	ver2138, _ := version.NewVersion("2.13.8")
	mockPxUtil.On("GetPortworxVersion", mock.AnythingOfType("*v1.StorageCluster")).Return(ver2138)

	// Set up expectations for StartPort method
	mockPxUtil.On("StartPort", mock.AnythingOfType("*v1.StorageCluster")).Return(pxutil.DefaultStartPort)

	// Create a sample StorageCluster instance
	cluster := &corev1.StorageCluster{}

	// Call the function being tested
	listeningPort := GetCCMListeningPort(cluster)

	// Define expected result
	expectedPort := defaultCCMListeningPortForPXge2138

	// Assert that the function returns the expected result
	assert.Equal(t, expectedPort, listeningPort)
}
