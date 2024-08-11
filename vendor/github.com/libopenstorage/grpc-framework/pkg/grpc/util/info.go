/*
Copyright 2022 Pure Storage

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

package util

import "strings"

// GetMethodInformation returns the service and API of a gRPC fullmethod string.
// For example, if the full method is:
//
//	/openstorage.api.OpenStorage<service>/<method>
//
// Then, to extract the service and api we would call it as follows:
//
//	s, a := GetMethodInformation("openstorage.api.OpenStorage", info.FullMethod)
//	   where info.FullMethod comes from the gRPC interceptor
func GetMethodInformation(constPath, fullmethod string) (service, api string) {
	parts := strings.Split(fullmethod, "/")

	if len(parts) > 1 {
		service = strings.TrimPrefix(strings.ToLower(parts[1]), strings.ToLower(constPath))
	}

	if len(parts) > 2 {
		api = strings.ToLower(parts[2])
	}

	return service, api
}
