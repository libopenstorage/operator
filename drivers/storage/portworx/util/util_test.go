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
	CACertFileName := stringPtr("util_testCA.crt")
	serverCertFileName := stringPtr("util_testServer.crt")
	serverKeyFileName := stringPtr("util_testServer.key")
	certRootPath := "/etc/pwx/"
	expectedArgs := []string{
		"-apirootca", certRootPath + *CACertFileName,
		"-apicert", certRootPath + *serverCertFileName,
		"-apikey", certRootPath + *serverKeyFileName,
		"-apidisclientauth",
	}
	cluster := testutil.CreatePodSpecWithTLS(CACertFileName, serverCertFileName, serverKeyFileName)
	// test
	args, err := GetOciMonArgumentsForTLS(cluster)
	// validate
	assert.Nil(t, err)
	assert.ElementsMatch(t, expectedArgs, args)

	// error scenarios
	// GetOciMonArgumentsForTLS expects that defaults have already been applied
	// setup
	cluster = testutil.CreatePodSpecWithTLS(CACertFileName, nil, serverKeyFileName)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.NotNil(t, err)

	cluster = testutil.CreatePodSpecWithTLS(nil, serverCertFileName, serverKeyFileName)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.NotNil(t, err)

	cluster = testutil.CreatePodSpecWithTLS(CACertFileName, serverCertFileName, nil)
	_, err = GetOciMonArgumentsForTLS(cluster)
	assert.NotNil(t, err)
}

func TestAuthEnabled(t *testing.T) {
	// security.enabled		security.auth.enabled		Auth enabled?
	// =============================================================
	// true					true						true
	// true					false						false
	// true					nil							true
	// false				true						true
	// false				false						false
	// false				nil							false

	testAuthEnabled(t, true, boolPtr(true), true)
	testAuthEnabled(t, true, boolPtr(false), false)
	testAuthEnabled(t, true, nil, true)
	testAuthEnabled(t, true, boolPtr(true), true)
	testAuthEnabled(t, true, boolPtr(false), false)
	testAuthEnabled(t, true, nil, true)

	// security.enabled		security.auth.enabled		TLS enabled?
	// =============================================================
	// security = nil									false
	cluster := createPodSpecWithAuth()
	cluster.Spec.Security = nil
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, false)

	// security.enabled		security.auth.enabled		TLS enabled?
	// =============================================================
	// true					security.auth = nil			true
	cluster = createPodSpecWithAuth()
	cluster.Spec.Security.Enabled = true
	cluster.Spec.Security.Auth = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, true)

	// security.enabled		security.tls.enabled		TLS enabled?
	// =============================================================
	// false				security.tls = nil			false
	cluster = createPodSpecWithAuth()
	cluster.Spec.Security.Enabled = false
	cluster.Spec.Security.Auth = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, false)
}

func TestIsTLSEnabledOnCluster(t *testing.T) {
	// security.enabled		security.tls.enabled		TLS enabled?
	// =============================================================
	// true					true						true
	// true					false						false
	// true					nil							true
	// false				true						true
	// false				false						false
	// false				nil							false

	testIsTLSEnabledOnCluster(t, true, boolPtr(true), true)
	testIsTLSEnabledOnCluster(t, true, boolPtr(false), false)
	testIsTLSEnabledOnCluster(t, true, nil, true)
	testIsTLSEnabledOnCluster(t, true, boolPtr(true), true)
	testIsTLSEnabledOnCluster(t, true, boolPtr(false), false)
	testIsTLSEnabledOnCluster(t, true, nil, true)

	// security.enabled		security.tls.enabled		TLS enabled?
	// =============================================================
	// security = nil									false
	cluster := testutil.CreatePodSpecWithTLS(nil, nil, nil)
	cluster.Spec.Security = nil
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, false)

	// security.enabled		security.tls.enabled		TLS enabled?
	// =============================================================
	// true					security.tls = nil			true
	cluster = testutil.CreatePodSpecWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = true
	cluster.Spec.Security.TLS = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, true)

	// security.enabled		security.tls.enabled		TLS enabled?
	// =============================================================
	// false				security.tls = nil			false
	cluster = testutil.CreatePodSpecWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = false
	cluster.Spec.Security.TLS = nil
	s, _ = json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual = IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, false)
}

// testIsTLSEnabledOnCluster is a helper method
func testIsTLSEnabledOnCluster(t *testing.T, securityEnabled bool, tlsEnabled *bool, expectedResult bool) {
	cluster := testutil.CreatePodSpecWithTLS(nil, nil, nil)
	cluster.Spec.Security.Enabled = securityEnabled
	cluster.Spec.Security.TLS.Enabled = tlsEnabled
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := IsTLSEnabledOnCluster(&cluster.Spec)
	assert.Equal(t, actual, expectedResult)
}

// testAuthEnabled is a helper method
func testAuthEnabled(t *testing.T, securityEnabled bool, authEnabled *bool, expectedResult bool) {
	cluster := createPodSpecWithAuth()
	cluster.Spec.Security.Enabled = securityEnabled
	cluster.Spec.Security.Auth.Enabled = authEnabled
	s, _ := json.MarshalIndent(cluster.Spec.Security, "", "\t")
	t.Logf("Security spec under test = \n, %v", string(s))
	actual := AuthEnabled(&cluster.Spec)
	assert.Equal(t, actual, expectedResult)
}

func createPodSpecWithAuth() *corev1.StorageCluster {
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
