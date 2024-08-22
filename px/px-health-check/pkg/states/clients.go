package states

import (
	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	StateKeyKubernetesClientSet     healthcheck.HealthStateDataKey = "k8s/kubernetes/client-go/interface"
	StateKeyKubernetesManagerClient healthcheck.HealthStateDataKey = "k8s/manager/dynamic/client"
	StateKeyPortworxSdkClient       healthcheck.HealthStateDataKey = "px/sdk/grpc/client"
)

// WithKubernetesClient sets up a simple Kubernetes client from the client-go
// package for the checks to use.
func WithKubernetesClientSet(hs *healthcheck.HealthCheckState, c kubernetes.Interface) {
	if hs == nil {
		return
	}
	hs.Set(StateKeyKubernetesClientSet, c)
}

// GetKubernetesClientSet provides the check with the simple client-go Kubernetes
// client if the HealthChecker caller provided it by calling WithKubernetesClientSet()
func GetKubernetesClientSet(hs *healthcheck.HealthCheckState) (kubernetes.Interface, bool) {
	if hs == nil {
		return nil, false
	}

	if v, ok := hs.Get(StateKeyKubernetesClientSet).(kubernetes.Interface); ok {
		return v, ok
	}
	return nil, false
}

// WithKubernetesManagerClient sets up a dynamic managed cached Kubernetes client
// for the checks to use.
func WithKubernetesManagerClient(hs *healthcheck.HealthCheckState, c client.Client) {
	if hs == nil {
		return
	}
	hs.Set(StateKeyKubernetesManagerClient, c)
}

// GetKubernetesManagerClient provides the check with a dynamaic, managed, and
// cached Kubernetes client if the HealthChecker caller provided it by calling
// WithKubernetesManagerClient()
func GetKubernetesManagerClient(hs *healthcheck.HealthCheckState) (client.Client, bool) {
	if hs == nil {
		return nil, false
	}

	if v, ok := hs.Get(StateKeyKubernetesManagerClient).(client.Client); ok {
		return v, ok
	}
	return nil, false
}

// WithPortworxSdkClient sets up a gRPC client to the Portworx SDK. This may be
// to a node or to a Kubernetes Service
func WithPortworxSdkClient(hs *healthcheck.HealthCheckState, c *grpc.ClientConn) {
	if hs == nil {
		return
	}
	hs.Set(StateKeyPortworxSdkClient, c)
}

// GetPortworxSdkClient provides the check with a the gRPC client to the
// Portworx node or Kubernetes Service if the HealthChecker caller provided it
// by calling WithPortworxSdkClient()
func GetPortworxSdkClient(hs *healthcheck.HealthCheckState) (*grpc.ClientConn, bool) {
	if hs == nil {
		return nil, false
	}

	if v, ok := hs.Get(StateKeyPortworxSdkClient).(*grpc.ClientConn); ok {
		return v, ok
	}
	return nil, false
}
