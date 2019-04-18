package portworx

import (
	storage "github.com/libopenstorage/operator/drivers/storage"
	"github.com/sirupsen/logrus"
)

const (
	// driverName is the name of the portworx storage driver implementation
	driverName = "pxd"
)

type portworx struct{}

func (p *portworx) String() string {
	return driverName
}

func (p *portworx) Init(_ interface{}) error {
	return nil
}

func (p *portworx) Stop() error {
	return nil
}

func init() {
	if err := storage.Register(driverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
