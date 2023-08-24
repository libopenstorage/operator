//go:build tools
// +build tools

package tools

import (
	// Needed to generate client code for APIs
	_ "k8s.io/code-generator"
	// Needed because of https://github.com/golang/mock/tree/v1.6.0#reflect-vendoring-error
	_ "github.com/golang/mock/mockgen/model"
)
