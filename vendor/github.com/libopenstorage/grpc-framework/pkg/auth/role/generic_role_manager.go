/*
Generirc
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

// For a full Role Manager example see github.com/libopenstorage/openstorage/pkg/role/sdkserviceapi.go

package role

import (
	"context"
	"fmt"

	grpcutil "github.com/libopenstorage/grpc-framework/pkg/grpc/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GenericRoleManager contains roles to verify for RBAC
type GenericRoleManager struct {
	tag   string
	roles map[string]*Role
}

// NewGenericRoleManager returns an RBAC API role manager
// that supports only the roles as defined by roles.
// `tag` is the tag in the service name. For example:
// If the gRPC info.FullMethod is /openstorage.api.OpenStorage<service>/<method>
// .   then the tag is "openstorage.api.OpenStorage".
// This will make it possible to only use the "<service>" name in the Rule.Service for convenience.
// If `tag` is "", then the info.FullMethod path must be provided in the Rule.Service
func NewGenericRoleManager(tag string, roles map[string]*Role) *GenericRoleManager {
	return &GenericRoleManager{
		tag:   tag,
		roles: roles,
	}
}

// Verify determines if the role has access to `fullmethod`
func (r *GenericRoleManager) Verify(ctx context.Context, roles []string, fullmethod string) error {

	// Check all roles
	for _, role := range roles {
		if rbac, ok := r.roles[role]; ok {
			if err := r.VerifyRules(rbac.Rules,
				r.tag,
				fullmethod); err == nil {
				return nil
			}
		}
	}

	return status.Errorf(codes.PermissionDenied, "Access denied to roles: %+s", roles)
}

// VerifyRules checks if the rules authorize use of the API called `fullmethod`
func (r *GenericRoleManager) VerifyRules(rules []*Rule, rootPath, fullmethod string) error {

	reqService, reqApi := grpcutil.GetMethodInformation(rootPath, fullmethod)

	// Look for denials first
	for _, rule := range rules {
		for _, service := range rule.Services {
			// if the service is denied, then return here
			if DenyRule(service, reqService) {
				return fmt.Errorf("access denied to service by role")
			}

			// If there is a match to the service now check the apis
			if MatchRule(service, reqService) {
				for _, api := range rule.Apis {
					if DenyRule(api, reqApi) {
						return fmt.Errorf("access denied to api by role")
					}
				}
			}
		}
	}

	// Look for permissions
	for _, rule := range rules {
		for _, service := range rule.Services {
			if MatchRule(service, reqService) {
				for _, api := range rule.Apis {
					if MatchRule(api, reqApi) {
						return nil
					}
				}
			}
		}
	}

	return fmt.Errorf("no accessible rule to authorize access found")
}
