/*
Copyright 2015 The Kubernetes Authors.
Modifications Copyright 2019 The Libopenstorage Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storagecluster

import (
	"testing"

	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestController_sortVolumeOrderInMiscArgs(t *testing.T) {
	tests := []struct {
		name    string
		cluster *corev1.StorageCluster
		want    string
	}{
		// TODO: Add test cases.
		{
			"blank misc args",
			&corev1.StorageCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						pxutil.AnnotationMiscArgs: "",
					},
				},
			},
			"",
		},
		{
			"unsorted volume",
			&corev1.StorageCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						pxutil.AnnotationMiscArgs: "-v /tmp/px-cores1:/var/cores -v /var/cores:/var/cores",
					},
				},
			},
			"-v /var/cores:/var/cores -v /tmp/px-cores1:/var/cores",
		},
		{
			"sorted already",
			&corev1.StorageCluster{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						pxutil.AnnotationMiscArgs: "-v /tmp/px-cores1:/var/cores -v /var/cores:/var/cores",
					},
				},
			},
			"-v /var/cores:/var/cores -v /tmp/px-cores1:/var/cores",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Controller{}
			c.sortVolumeOrderInMiscArgs(tt.cluster)
			require.Equal(t, tt.cluster.Annotations[pxutil.AnnotationMiscArgs], tt.want)
		})
	}
}
