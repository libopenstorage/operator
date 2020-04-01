// Add a tools.go file to track tools dependencies.
// See:
//   https://github.com/golang/go/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module

// +build tools

package tools

import (
	// Needed for codegen dependency
	_ "k8s.io/code-generator"
)
