package portworxdiag

import (
	"fmt"
	"reflect"

	"github.com/libopenstorage/openstorage/api"
)

// This file contains various gomock matcher utilities that can be used to better check calls
// to the mock GRPC server.

type volumeInspectRequestMatcher struct {
	volumeID string
}

func (v volumeInspectRequestMatcher) Matches(x interface{}) bool {
	req, ok := x.(*api.SdkVolumeInspectRequest)
	if !ok {
		return false
	}
	return req.GetVolumeId() == v.volumeID
}

func (v volumeInspectRequestMatcher) String() string {
	return "matches volume inspect request with volume ID " + v.volumeID
}

func VolumeInspectRequestWithVolumeID(volumeID string) *volumeInspectRequestMatcher {
	return &volumeInspectRequestMatcher{volumeID}
}

type volumeInspectWithFiltersRequestWithLabelsMatcher struct {
	labels map[string]string
}

func (v volumeInspectWithFiltersRequestWithLabelsMatcher) Matches(x interface{}) bool {
	req, ok := x.(*api.SdkVolumeInspectWithFiltersRequest)
	if !ok {
		return false
	}
	// Deep-compare the labels
	return reflect.DeepEqual(req.GetLabels(), v.labels)
}

func (v volumeInspectWithFiltersRequestWithLabelsMatcher) String() string {
	return fmt.Sprintf("matches volume inspect request with labels %v", v.labels)
}

func VolumeInspectWithFilterRequestWithLabels(labels map[string]string) *volumeInspectWithFiltersRequestWithLabelsMatcher {
	return &volumeInspectWithFiltersRequestWithLabelsMatcher{labels}
}
