package cloud_provider

type pureProvider struct{}

func newPureProvider() Provider {
	return pureProvider{}
}

func (pureProvider) GetDefaultDataDrives() *[]string {
	return stringSlicePtr([]string{
		"type=pure-block,size=150"})
}

func (pureProvider) GetDefaultMetadataDrive() *string {
	return stringPtr("type=pure-block,size=64")
}

func (pureProvider) GetDefaultKvdbDrive() *string {
	return stringPtr("type=pure-block,size=32")
}

func (pureProvider) GetDefaultJournalDrive() *string {
	return stringPtr("type=pure-block,size=32")
}
