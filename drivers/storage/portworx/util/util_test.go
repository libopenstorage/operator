package util

import (
	"encoding/json"
	"testing"

	version "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

func TestGetOciMonArgumentsForTLS(t *testing.T) {
	// setup
	caCertFileName := stringPtr("/etc/pwx/util_testCA.crt")
	serverCertFileName := stringPtr("/etc/pwx/util_testServer.crt")
	serverKeyFileName := stringPtr("/etc/pwx/util_testServer.key")
	expectedArgs := []string{
		"-apirootca", *caCertFileName,
		"-apicert", *serverCertFileName,
		"-apikey", *serverKeyFileName,
		"-apidisclientauth",
	}
	cluster := testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, serverKeyFileName)
	// test
	args, err := GetOciMonArgumentsForTLS(cluster)
	// validate
	assert.Nil(t, err)
	assert.ElementsMatch(t, expectedArgs, args)

	// mixture of file and k8s secret as sources for tls certs

	// rootCA from k8s secret, serverCert and serverKey from file
	// setup
	cluster = testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, serverKeyFileName)
	tls := cluster.Spec.Security.TLS
	tls.RootCA = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
			SecretKey:  "somekey",
		},
	}
	expectedArgs = []string{
		"-apirootca", DefaultTLSCACertMountPath + "somekey",
		"-apicert", *serverCertFileName,
		"-apikey", *serverKeyFileName,
		"-apidisclientauth",
	}
	// test
	args, err = GetOciMonArgumentsForTLS(cluster)
	// validate
	assert.Nil(t, err)
	assert.ElementsMatch(t, expectedArgs, args)

	// serverCert and serverKey from k8s secret, rootCA from file
	// setup
	cluster = testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, serverKeyFileName)
	tls = cluster.Spec.Security.TLS
	tls.ServerCert = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "somesecret",
			SecretKey:  "somekey",
		},
	}
	tls.ServerKey = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "someothersecret",
			SecretKey:  "someotherkey",
		},
	}
	expectedArgs = []string{
		"-apirootca", *caCertFileName,
		"-apicert", DefaultTLSServerCertMountPath + "somekey",
		"-apikey", DefaultTLSServerKeyMountPath + "someotherkey",
		"-apidisclientauth",
	}
	// test
	args, err = GetOciMonArgumentsForTLS(cluster)
	// validate
	assert.Nil(t, err)
	assert.ElementsMatch(t, expectedArgs, args)

	// error scenarios
	// GetOciMonArgumentsForTLS expects that defaults have already been applied
	// setup
	cluster = testutil.CreateClusterWithTLS(caCertFileName, nil, serverKeyFileName)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.NotNil(t, err)

	cluster = testutil.CreateClusterWithTLS(caCertFileName, serverCertFileName, nil)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.NotNil(t, err)

	// ca can be null if cert/key specified
	cluster = testutil.CreateClusterWithTLS(nil, serverCertFileName, serverKeyFileName)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.Nil(t, err)

	// test auto-ssl setup
	cluster.Spec.Version = "3.0.0"
	cluster.Spec.Security.TLS.ServerCert = nil
	cluster.Spec.Security.TLS.ServerKey = nil
	cluster.Spec.Security.TLS.RootCA = nil
	args, err = GetOciMonArgumentsForTLS(cluster)
	// validate
	assert.Nil(t, err)
	assert.ElementsMatch(t, []string{"--auto-tls"}, args)
}

func TestAuthEnabled(t *testing.T) {
	// security.enabled    security.auth.enabled        Auth enabled?
	// ==============================================================
	// true                true                         true
	// true                false                        false
	// true                nil                          true
	// false               true                         false
	// false               false                        false
	// false               nil                          false

	testAuthEnabled(t, true, boolPtr(true), true)
	testAuthEnabled(t, true, boolPtr(false), false)
	testAuthEnabled(t, true, nil, true)
	testAuthEnabled(t, false, boolPtr(true), false)
	testAuthEnabled(t, false, boolPtr(false), false)
	testAuthEnabled(t, false, nil, false)

	// security.enabled    security.auth.enabled        Auth enabled?
	// ==============================================================
	// nil                                              false
	cluster := createClusterWithAuth()
	cluster.Spec.Security = nil
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, false)

	// security.enabled    security.auth.enabled        Auth enabled?
	// ==============================================================
	// true                nil                          true
	cluster = createClusterWithAuth()
	cluster.Spec.Security.Enabled = true
	cluster.Spec.Security.Auth = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, true)

	// security.enabled    security.auth.enabled        Auth enabled?
	// ==============================================================
	// false                nil                          false
	cluster = createClusterWithAuth()
	cluster.Spec.Security.Enabled = false
	cluster.Spec.Security.Auth = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, false)
}

func TestIsTLSEnabledOnCluster(t *testing.T) {
	// security.enabled    security.tls.enabled         TLS enabled?
	// =============================================================
	// true                true                         true
	// true                false                        false
	// true                nil                          false
	// false               true                         false
	// false               false                        false
	// false               nil                          false
	testIsTLSEnabledOnCluster(t, true, boolPtr(true), true)
	testIsTLSEnabledOnCluster(t, true, boolPtr(false), false)
	testIsTLSEnabledOnCluster(t, true, nil, false)
	testIsTLSEnabledOnCluster(t, false, boolPtr(true), false)
	testIsTLSEnabledOnCluster(t, false, boolPtr(false), false)
	testIsTLSEnabledOnCluster(t, false, nil, false)

	// security.enabled    security.tls.enabled          TLS enabled?
	// ==============================================================
	// nil                                               false
	cluster := testutil.CreateClusterWithTLS(nil, nil, nil)
	cluster.Spec.Security = nil
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, false)

	// security.enabled    security.tls.enabled          TLS enabled?
	// ==============================================================
	// true                nil                           false
	cluster = testutil.CreateClusterWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = true
	cluster.Spec.Security.TLS = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, false)

	// security.enabled    security.tls.enabled          TLS enabled?
	// ==============================================================
	// false               nil                           false
	cluster = testutil.CreateClusterWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = false
	cluster.Spec.Security.TLS = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, false)
}

// testIsTLSEnabledOnCluster is a helper method
func testIsTLSEnabledOnCluster(t *testing.T, securityEnabled bool, tlsEnabled *bool, expectedResult bool) {
	cluster := testutil.CreateClusterWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = securityEnabled
	cluster.Spec.Security.TLS.Enabled = tlsEnabled
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, expectedResult)
}

// testAuthEnabled is a helper method
func testAuthEnabled(t *testing.T, securityEnabled bool, authEnabled *bool, expectedResult bool) {
	cluster := createClusterWithAuth()
	cluster.Spec.Security.Enabled = securityEnabled
	cluster.Spec.Security.Auth.Enabled = authEnabled
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, expectedResult)
}

func TestIsEmptyOrNilCertLocation(t *testing.T) {
	obj := &corev1.CertLocation{
		FileName: stringPtr("somefile"),
	}
	assert.False(t, IsEmptyOrNilCertLocation(obj))

	obj = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "somename",
			SecretKey:  "somekey",
		},
	}
	assert.False(t, IsEmptyOrNilCertLocation(obj))

	obj = &corev1.CertLocation{}
	assert.True(t, IsEmptyOrNilCertLocation(obj))

	obj = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{},
	}
	assert.True(t, IsEmptyOrNilCertLocation(obj))

	obj = &corev1.CertLocation{
		SecretRef: &corev1.SecretRef{
			SecretName: "somename",
		},
	}
	assert.True(t, IsEmptyOrNilCertLocation(obj))

	obj = nil
	assert.True(t, IsEmptyOrNilCertLocation(obj))

}

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

func createClusterWithAuth() *corev1.StorageCluster {
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-system",
		},
		Spec: corev1.StorageClusterSpec{
			Security: &corev1.SecuritySpec{
				Enabled: true,
				Auth: &corev1.AuthSpec{
					Enabled: boolPtr(true),
				},
			},
		},
	}
	return cluster
}

func stringPtr(val string) *string {
	return &val
}

func boolPtr(val bool) *bool {
	return &val
}
