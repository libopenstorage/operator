package storage

import (
	"github.com/libopenstorage/operator/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Driver defines an external storage driver interface.
// Any driver that wants to be used with openstorage operator needs
// to implement these interfaces.
type Driver interface {
	// Init initializes the storage driver
	Init(interface{}) error

	// String returns the string name of the driver
	String() string

	// Stop the driver
	Stop() error
}

var (
	storageDrivers = make(map[string]Driver)
)

// Register registers the given storage driver
func Register(name string, d Driver) error {
	logrus.Debugf("Registering storage driver: %v", name)
	storageDrivers[name] = d
	return nil
}

// Get an external storage driver to be used with the operator
func Get(name string) (Driver, error) {
	d, ok := storageDrivers[name]
	if ok {
		return d, nil
	}
	return nil, &errors.ErrNotFound{
		ID:   name,
		Type: "StorageDriver",
	}
}
