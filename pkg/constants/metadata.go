package constants

const (
	OperatorPrefix     = "operator.libopenstorage.org"
	LabelKeyName       = OperatorPrefix + "/name"
	LabelKeyDriverName = OperatorPrefix + "/driver"
	// AnnotationDisableStorage annotation to disable the storage pods from running.
	// Defaults to false value.
	AnnotationDisableStorage    = OperatorPrefix + "/disable-storage"
	ClusterAPIMachineAnnotation = "cluster.k8s.io/machine"
	StoragePodLabelKey          = "storage"
	KVDBPodLabelKey             = "kvdb"
	TrueLabelValue              = "true"
)
