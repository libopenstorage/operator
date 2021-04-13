package util

import (
	"encoding/json"
	"testing"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestPartialSecretRef(t *testing.T) {
	// happy
	obj := &corev1.SecretRef{
		SecretName: "a",
		SecretKey:  "b",
	}
	assert.False(t, IsPartialSecretRef(obj))

	// no valid secret key
	obj = &corev1.SecretRef{
		SecretName: "a",
		SecretKey:  "",
	}
	assert.True(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{
		SecretName: "a",
	}
	assert.True(t, IsPartialSecretRef(obj))

	// no valid secret name
	obj = &corev1.SecretRef{
		SecretName: "",
		SecretKey:  "b",
	}
	assert.True(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{
		SecretKey: "b",
	}
	assert.True(t, IsPartialSecretRef(obj))

	// no valid secret key or name. Return false because it's empty, not partial
	obj = &corev1.SecretRef{
		SecretName: "",
		SecretKey:  "",
	}
	assert.False(t, IsPartialSecretRef(obj))

	obj = &corev1.SecretRef{}
	assert.False(t, IsPartialSecretRef(obj))

	obj = nil
	assert.False(t, IsPartialSecretRef(obj))
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
