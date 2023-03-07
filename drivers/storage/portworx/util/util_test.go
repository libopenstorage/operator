package util

import (
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

func TestGetServiceTypeFromAnnotation(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "px-cluster",
		},
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: ";",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: ":",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "ClusterIP",
	}
	require.Equal(t, v1.ServiceTypeClusterIP, ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceTypeClusterIP, ServiceType(cluster, PortworxServiceName))
	require.Equal(t, v1.ServiceTypeClusterIP, ServiceType(cluster, PortworxKVDBServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "Invalid",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxKVDBServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "portworx-service:LoadBalancer",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceTypeLoadBalancer, ServiceType(cluster, PortworxServiceName))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxKVDBServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "portworx-kvdb-service:ClusterIP;",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, PortworxServiceName))
	require.Equal(t, v1.ServiceTypeClusterIP, ServiceType(cluster, PortworxKVDBServiceName))

	cluster.Annotations = map[string]string{
		AnnotationServiceType: "portworx-service:LoadBalancer;portworx-kvdb-service:ClusterIP;other-services:Invalid",
	}
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, ""))
	require.Equal(t, v1.ServiceTypeLoadBalancer, ServiceType(cluster, PortworxServiceName))
	require.Equal(t, v1.ServiceTypeClusterIP, ServiceType(cluster, PortworxKVDBServiceName))
	require.Equal(t, v1.ServiceType(""), ServiceType(cluster, "other-services"))
}

func TestSplitPxProxyHostPort(t *testing.T) {
	// Valid cases
	address, port, err := SplitPxProxyHostPort("http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", address)
	require.Equal(t, "1234", port)

	address, port, err = SplitPxProxyHostPort("http://http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", address)
	require.Equal(t, "1234", port)

	address, port, err = SplitPxProxyHostPort("1.2.3.4:1234")
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", address)
	require.Equal(t, "1234", port)

	address, port, err = SplitPxProxyHostPort("[1:2:3:4:5:6:7:8]:1234")
	require.NoError(t, err)
	require.Equal(t, "1:2:3:4:5:6:7:8", address)
	require.Equal(t, "1234", port)

	// Invalid cases
	_, _, err = SplitPxProxyHostPort("")
	require.Error(t, err)

	_, _, err = SplitPxProxyHostPort("http://address")
	require.Error(t, err)

	_, _, err = SplitPxProxyHostPort("address:")
	require.Error(t, err)

	_, _, err = SplitPxProxyHostPort("1:2:3:4:5:6:7:8")
	require.Error(t, err)

	_, _, err = SplitPxProxyHostPort(":1234")
	require.Error(t, err)

	_, _, err = SplitPxProxyHostPort("user:password@host:1234")
	require.Error(t, err)
}
