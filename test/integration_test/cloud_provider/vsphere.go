package cloud_provider

type vsphereProvider struct{}

func newVsphereProvider() Provider {
	return vsphereProvider{}
}

func (vsphereProvider) GetDefaultDataDrives() *[]string {
	return stringSlicePtr([]string{
		"type=thin,size=49",
		"type=zeroedthick,size=59"})
}

func (vsphereProvider) GetDefaultMetadataDrive() *string {
	return stringPtr("type=zeroedthick,size=64")
}

func (vsphereProvider) GetDefaultKvdbDrive() *string {
	return stringPtr("type=zeroedthick,size=32")
}
