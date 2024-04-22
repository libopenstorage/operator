package util

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
)

func TestVersionToSemver(t *testing.T) {

	tests := []struct {
		ver      string
		expected string
	}{
		{
			ver:      "0",
			expected: "0",
		},
		{
			ver:      "0.0",
			expected: "0.0",
		},
		{
			ver:      "0.0.0",
			expected: "0.0.0",
		},
		{
			ver:      "0.0.0.0",
			expected: "0.0.0",
		},
		{
			ver:      "0.0.0.0.0",
			expected: "0.0.0",
		},
		{
			ver:      "0.0.0-100",
			expected: "0.0.0-100",
		},
		{
			ver:      "4.20.13-1.el7.elrepo.x86_64",
			expected: "4.20.13-1",
		},
		{
			ver:      "4.20.13+234.234",
			expected: "4.20.13+234",
		},
		{
			ver:      "v1.25.16+9946c63",
			expected: "v1.25.16+9946c63",
		},
	}

	for _, test := range tests {
		v := VersionToSemver(test.ver)
		_, err := semver.NewVersion(v)
		assert.NoError(t, err)
		assert.Equal(t, test.expected, v)
	}
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		ver    string
		limit  string
		expect bool
	}{
		{
			ver: "4.20.13-1.el7.elrepo.x86_64",
			// version can be prerelease according to semver
			limit:  ">= 4.18.0-0",
			expect: true,
		},
		{
			ver: "4.20.13-1.el7.elrepo.x86_64",
			// version must be released and not preleased
			limit:  ">= 4.18.0",
			expect: false,
		},
		{
			ver:    "4.20.13-100.el7.elrepo.x86_64",
			limit:  ">= 4.20.13-14",
			expect: true,
		},
		{
			ver:    "4.20.13",
			limit:  ">= 4.20.13-0",
			expect: true,
		},
		{
			ver:    "4.25.0",
			limit:  ">= 4.20.13-0",
			expect: true,
		},
		{
			ver:    "2.26.0",
			limit:  ">= 4.20.13-0",
			expect: false,
		},
		{
			ver:    "v1.25.16+9946c63",
			limit:  ">= 1.25.0",
			expect: true,
		},
		{
			ver:    "v1.25.16+9946c63",
			limit:  ">= 1.25.17",
			expect: false,
		},
		{
			ver:    "v1.25.16+9946c63",
			limit:  "< 1.30.0",
			expect: true,
		},
		{
			ver:    "v1.25.16+9946c63",
			limit:  "< 1.14.0",
			expect: false,
		},
	}

	for _, test := range tests {
		v := VersionToSemver(test.ver)
		sm, err := semver.NewVersion(v)
		assert.NoError(t, err)

		constraint, err := semver.NewConstraint(test.limit)
		assert.NoError(t, err)

		assert.Equal(t, test.expect, constraint.Check(sm), test)
	}
}
