package drivers

// Driver specifies the most basic methods to be implemented by a Storage cluster driver.
type Driver interface {
	// Init the driver.
	Init() error
}
