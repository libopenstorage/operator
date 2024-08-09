/*
Generic role manager
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

package role

const (
	SystemAdminRoleName = "system.admin"
	SystemGuestRoleName = "system.guest"
)

var (
	// Roles are the default roles to load on system startup
	// Should be prefixed by `system.` to avoid collisions
	DefaultRoles = map[string]*Role{
		// system:admin role can run any command
		SystemAdminRoleName: &Role{
			Rules: []*Rule{
				&Rule{
					Services: []string{"*"},
					Apis:     []string{"*"},
				},
			},
		},

		// system:guest role is used for any unauthenticated user.
		// They can only use standard volume lifecycle commands.
		SystemGuestRoleName: &Role{
			Rules: []*Rule{
				&Rule{
					Services: []string{"!*"},
					Apis:     []string{"!*"},
				},
			},
		},
	}
)

// NewDefaultGenericRoleManager returns an RBAC API role manager
// that supports only the roles as defined by DefaultRoles
func NewDefaultGenericRoleManager() *GenericRoleManager {
	return NewGenericRoleManager("", DefaultRoles)
}
