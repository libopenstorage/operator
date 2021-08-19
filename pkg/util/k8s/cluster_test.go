package k8s

import (
	"context"
	"testing"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
	ocp_configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIsClusterBeingUpgraded(t *testing.T) {
	testCases := []struct {
		name            string
		clusterVersions []*ocp_configv1.ClusterVersion
		expectedOut     bool
	}{
		{
			name:            "no-cluster-versions-present",
			expectedOut:     false,
			clusterVersions: nil,
		},
		{
			name:        "cluster-version-without-desired-update",
			expectedOut: false,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
				},
			},
		},
		{
			name:        "cluster-version-without-desired-update-version",
			expectedOut: false,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
					Spec: ocp_configv1.ClusterVersionSpec{
						DesiredUpdate: &ocp_configv1.Update{},
					},
				},
			},
		},
		{
			name:        "cluster-version-without-history",
			expectedOut: false,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
					Spec: ocp_configv1.ClusterVersionSpec{
						DesiredUpdate: &ocp_configv1.Update{
							Version: "1.2.3",
						},
					},
					Status: ocp_configv1.ClusterVersionStatus{},
				},
			},
		},
		{
			name:        "cluster-version-without-matching-history",
			expectedOut: false,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
					Spec: ocp_configv1.ClusterVersionSpec{
						DesiredUpdate: &ocp_configv1.Update{
							Version: "1.2.3",
						},
					},
					Status: ocp_configv1.ClusterVersionStatus{
						History: []ocp_configv1.UpdateHistory{
							{
								Version: "3.2.1",
								State:   ocp_configv1.CompletedUpdate,
							},
						},
					},
				},
			},
		},
		{
			name:        "cluster-version-with-completed-state",
			expectedOut: false,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
					Spec: ocp_configv1.ClusterVersionSpec{
						DesiredUpdate: &ocp_configv1.Update{
							Version: "1.2.3",
						},
					},
					Status: ocp_configv1.ClusterVersionStatus{
						History: []ocp_configv1.UpdateHistory{
							{
								Version: "1.2.3",
								State:   ocp_configv1.CompletedUpdate,
							},
						},
					},
				},
			},
		},
		{
			name:        "cluster-version-with-partial-state",
			expectedOut: true,
			clusterVersions: []*ocp_configv1.ClusterVersion{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "version",
					},
					Spec: ocp_configv1.ClusterVersionSpec{
						DesiredUpdate: &ocp_configv1.Update{
							Version: "1.2.3",
						},
					},
					Status: ocp_configv1.ClusterVersionStatus{
						History: []ocp_configv1.UpdateHistory{
							{
								Version: "1.2.3",
								State:   ocp_configv1.PartialUpdate,
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			k8sClient := testutil.FakeK8sClient()
			for _, cv := range tc.clusterVersions {
				err := k8sClient.Create(context.TODO(), cv, &client.CreateOptions{})
				require.NoError(t, err)
			}

			out, err := IsClusterBeingUpgraded(k8sClient)

			require.NoError(t, err)
			require.Equal(t, tc.expectedOut, out)
		})
	}
}
