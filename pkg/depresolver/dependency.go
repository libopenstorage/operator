package depresolver

// an empty array represents that it will wait for all other pods with portworx label
const DefaultDependencies = `{
	"px-csi-driver": ["*"]
}`
