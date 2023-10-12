package cloud_provider

type azureProvider struct{}

func newAzureProvider() Provider {
	return azureProvider{}
}

func (azureProvider) GetDefaultDataDrives() *[]string {
	return stringSlicePtr([]string{
		"type=Standard_LRS,size=49",
		"type=Premium_LRS,size=59"})
}

func (azureProvider) GetDefaultMetadataDrive() *string {
	return stringPtr("type=Premium_LRS,size=64")
}

func (azureProvider) GetDefaultKvdbDrive() *string {
	return stringPtr("type=Premium_LRS,size=150")
}

func (azureProvider) GetDefaultJournalDrive() *string {
	return stringPtr("type=Standard_LRS,size=32")
}
