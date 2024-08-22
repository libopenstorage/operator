package util

import "strings"

// VersionToSemver cleans up a version information so that
// it can be compatible with Semver utilities.
// See:
// - https://semver.org/
// - https://github.com/Masterminds/semver
func VersionToSemver(kv string) string {
	parts := strings.Split(kv, ".")
	if len(parts) > 3 {
		return strings.Join(parts[0:3], ".")
	}
	return strings.Join(parts, ".")
}
