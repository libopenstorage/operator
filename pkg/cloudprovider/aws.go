package cloudprovider

import (
	"github.com/libopenstorage/cloudops"
)

type aws struct {
	defaultProvider
}

func (a *aws) Name() string {
	return string(cloudops.AWS)
}
