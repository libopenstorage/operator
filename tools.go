//go:build tools
// +build tools

package tools

import (
	// Needed to generate client code for APIs
	_ "k8s.io/code-generator"
	// Needed to generate CRD manifests and deepcopy functions for objects
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
	// Needed because of https://github.com/golang/mock/tree/v1.6.0#reflect-vendoring-error
	_ "github.com/golang/mock/mockgen/model"
	// Go linter
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"
)
