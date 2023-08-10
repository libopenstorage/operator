package util

import (
	"testing"

	version "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
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

func TestParsePxProxyURL(t *testing.T) {
	// Valid cases
	host, port, authHeader, err := ParsePxProxyURL("http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("http://http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("https://http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("1.2.3.4:1234")
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("[1:2:3:4:5:6:7:8]:1234")
	require.NoError(t, err)
	require.Equal(t, "1:2:3:4:5:6:7:8", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("https://user:password@http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "Basic dXNlcjpwYXNzd29yZA==", authHeader)

	host, port, authHeader, err = ParsePxProxyURL("http://user:password@http.proxy.address:1234")
	require.NoError(t, err)
	require.Equal(t, "http.proxy.address", host)
	require.Equal(t, "1234", port)
	require.Equal(t, "Basic dXNlcjpwYXNzd29yZA==", authHeader)

	// Invalid cases
	_, _, _, err = ParsePxProxyURL("")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("http://address")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("address:")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("1:2:3:4:5:6:7:8")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL(":1234")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("user:password@http.proxy.address:1234")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("https://user:password@address")
	require.Error(t, err)

	_, _, _, err = ParsePxProxyURL("https://user:password@:1234")
	require.Error(t, err)
}

func TestGetPortworxVersion(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "px-cluster",
			Namespace:   "kube-system",
			Annotations: map[string]string{},
		},
		Spec: corev1.StorageClusterSpec{},
	}

	pxVer30, _ := version.NewVersion("3.0")

	// Check image version for 3.0
	logrus.Infof("Oci-Monitor version check...")
	cluster.Spec.Image = "portworx/oci-monitor:3.0.0"
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	// Check PX_IMAGE ENV
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "PX_IMAGE",
			Value: "portworx/px-base-enterprise:3.0.0",
		},
	}
	logrus.Infof("PX_IMAGE env version check with oci-mon image specified...")
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	logrus.Infof("PX_IMAGE env version check without oci-mon image specified...")
	cluster.Spec.Image = ""
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	// Check Annotation version
	logrus.Infof("px-version annotation check with oci-mage with invalid tag...")
	cluster.Spec.Image = "portworx/oci-monitor:31f6e43_038005f"
	cluster.Spec.Env = []v1.EnvVar{}
	cluster.Annotations[AnnotationPXVersion] = "3.0.0"
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	logrus.Infof("px-version annotation check without oci-mage...")
	cluster.Spec.Image = ""
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	// Check Manifest URL
	logrus.Infof("PX_RELEASE_MANIFEST_URL Env version check with oci-mage with invalid tag...")
	cluster.Spec.Image = "portworx/oci-monitor:31f6e43_038005f"
	cluster.Annotations = map[string]string{}
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  "PX_RELEASE_MANIFEST_URL",
			Value: "https://edge-install.portworx.com/3.0.0/version",
		},
	}
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))

	logrus.Infof("PX_RELEASE_MANIFEST_URL Env version check without oci-mage...")
	cluster.Spec.Image = ""
	assert.True(t, GetPortworxVersion(cluster).Equal(pxVer30))
}

func TestIsPrivileged(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "px-cluster",
			Namespace:   "kube-system",
			Annotations: map[string]string{},
		},
		Spec: corev1.StorageClusterSpec{},
	}

	assert.True(t, IsPrivileged(cluster))

	data := []struct {
		privileged string
		expect     bool
	}{
		{"", true},
		{"foo", true},
		{"true", true},
		{"false", false},
		{"0", false},
	}

	for i, td := range data {
		cluster.ObjectMeta.Annotations[AnnotationIsPrivileged] = td.privileged
		got := IsPrivileged(cluster)
		assert.Equal(t, td.expect, got, "Failed expectations for #%d/%#v", i, td)
	}
}

func TestGetTLSMinVersion(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "px-cluster",
			Namespace:   "kube-system",
			Annotations: make(map[string]string),
		},
		Spec: corev1.StorageClusterSpec{},
	}

	ret, err := GetTLSMinVersion(cluster)
	assert.NoError(t, err)
	assert.Empty(t, ret)

	data := []struct {
		input     string
		expectOut string
		expectErr bool
	}{
		{"", "", false},
		{"VersionTLS10", "VersionTLS10", false},
		{"  VersionTLS10", "VersionTLS10", false},
		{"VersionTLS10\t", "VersionTLS10", false},
		{" VersionTLS11 ", "VersionTLS11", false},
		{"VersionTLS12", "VersionTLS12", false},
		{"VersionTLS13", "VersionTLS13", false},
		{"versiontls13", "VersionTLS13", false},
		{"VERSIONTLS13", "VersionTLS13", false},
		{"VersionTLS14", "", true},
		{"VersionTLS3", "", true},
	}
	for i, td := range data {
		cluster.ObjectMeta.Annotations[AnnotationServerTLSMinVersion] = td.input
		ret, err = GetTLSMinVersion(cluster)
		if td.expectErr {
			assert.Error(t, err, "Expecting error for #%d / %v", i+1, td)
		} else {
			assert.NoError(t, err, "Expecting NO error for #%d / %v", i+1, td)
			assert.Equal(t, td.expectOut, ret, "Unexpected result for #%d / %v", i+1, td)
		}
	}
}

func TestGetTLSCipherSuites(t *testing.T) {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "px-cluster",
			Namespace:   "kube-system",
			Annotations: make(map[string]string),
		},
		Spec: corev1.StorageClusterSpec{},
	}

	ret, err := GetTLSCipherSuites(cluster)
	assert.NoError(t, err)
	assert.Empty(t, ret)

	data := []struct {
		input     string
		expectOut string
		expectErr bool
	}{
		{"", "", false},
		{
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			false,
		},
		{
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384 TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384 TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			false,
		},
		{
			"	  TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384 :: TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384 \t",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
			false,
		},
		{
			"tls_ecdhe_ecdsa_with_aes_256_gcm_sha384 tls_ecdhe_rsa_with_aes_256_gcm_sha384 tls_ecdhe_rsa_with_aes_128_gcm_sha256",
			"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			false,
		},
		{"x,y", "", true},
	}
	for i, td := range data {
		cluster.ObjectMeta.Annotations[AnnotationServerTLSCipherSuites] = td.input
		ret, err = GetTLSCipherSuites(cluster)
		if td.expectErr {
			assert.Error(t, err, "Expecting error for #%d / %v", i+1, td)
		} else {
			assert.NoError(t, err, "Expecting NO error for #%d / %v", i+1, td)
			assert.Equal(t, td.expectOut, ret, "Unexpected result for #%d / %v", i+1, td)
		}
	}
}
