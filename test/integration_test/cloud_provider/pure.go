package cloud_provider

type pureProvider struct{}

func newPureProvider() Provider {
	return pureProvider{}
}

func (pureProvider) GetDefaultDataDrives() *[]string {
	return stringSlicePtr([]string{
		"type=thin,size=49",
		"type=zeroedthick,size=59"})
}

func (pureProvider) GetDefaultMetadataDrive() *string {
	return stringPtr("type=zeroedthick,size=64")
}

func (pureProvider) GetDefaultKvdbDrive() *string {
	return stringPtr("type=zeroedthick,size=32")
}

func (pureProvider) GetDefaultJournalDrive() *string {
	return stringPtr("type=zeroedthick,size=32")
}
