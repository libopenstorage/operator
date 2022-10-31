package storagenodescount

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

const (
	// SncYamlPath is the name of the yaml that will contain the decision matrix for storage nodes
	SncYamlPath = "storage_nodes_count.yaml"
	// YamlFilePath is the path where unit tests will find in this directory
	YamlFilePath = "generator/" + SncYamlPath
	// TestFilePath is where the unit test in controller_test will find the yaml file
	TestFilePath = "../../controller/storagecluster/" + SncYamlPath
)

// MatrixRow defines an entry in the storage node count decision matrix.
type MatrixRow struct {
	// TotalNodesMin is the minimum total nodes.
	TotalNodesMin uint64 `json:"total_nodes_min" yaml:"total_nodes_min"`
	// TotalNodesMax is the maximum total nodes.
	TotalNodesMax uint64 `json:"total_nodes_max" yaml:"total_nodes_max"`
	// StorageNodeCount is the default number of storage nodes for the above range
	StorageNodeCount uint64 `json:"storage_node_count" yaml:"storage_node_count"`
}

// DecisionMatrix is used to determine a sane number of storage nodes for the give range of total nodes in the cluster.
type DecisionMatrix struct {
	// Rows of the decision matrix
	Rows []MatrixRow `json:"rows" yaml:"rows"`
}

// StorageNodesMatrixParser parses a cloud storage decision matrix from yamls
// to StorageDecisionMatrix objects defined in cloudops
type StorageNodesMatrixParser interface {
	// MarshalToYaml marshals the provided StorageDecisionMatrix
	// to a yaml file at the provided path
	MarshalToYaml(*DecisionMatrix, string) error
	// UnmarshalFromYaml unmarshals the yaml file at the provided path
	// into a StorageDecisionMatrix
	UnmarshalFromYaml(string) (*DecisionMatrix, error)
	// MarshalToBytes marshals the provided StorageDecisionMatrix to bytes
	MarshalToBytes(*DecisionMatrix) ([]byte, error)
	// UnmarshalFromBytes unmarshals the given yaml bytes into a StorageDecisionMatrix
	UnmarshalFromBytes([]byte) (*DecisionMatrix, error)
}

// GetStorageNodesCount returns the number of recommended storage nodes given the total nodes in the cluster and the number of zones
func GetStorageNodesCount(totalNodes uint64, totalZones uint64) (uint64, error) {
	var matrix *DecisionMatrix
	var err error
	for _, path := range []string{SncYamlPath, YamlFilePath} {
		matrix, err = NewStorageNodesMatrixParser().UnmarshalFromYaml(path)
		if err == nil {
			logrus.Infof("MYD: %v", path)
			break
		}
	}
	if err != nil {
		return 0, err
	}
	for _, row := range matrix.Rows {
		if row.TotalNodesMin <= totalNodes && row.TotalNodesMax >= totalNodes {
			storageNodes := row.StorageNodeCount
			return storageNodes / totalZones, nil
		}
	}
	logrus.Infof("MYD %v", matrix.Rows)
	return 0, fmt.Errorf("totalNodes out of range")
}

// NewStorageNodesMatrixParser returns an implementation of StorageNodesMatrixParser
func NewStorageNodesMatrixParser() StorageNodesMatrixParser {
	return &snmParser{}
}

type snmParser struct{}

func (s *snmParser) MarshalToYaml(
	matrix *DecisionMatrix,
	filePath string,
) error {
	yamlBytes, err := s.MarshalToBytes(matrix)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filePath, yamlBytes, 0777)
}

func (s *snmParser) UnmarshalFromYaml(
	filePath string,
) (*DecisionMatrix, error) {
	yamlBytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return s.UnmarshalFromBytes(yamlBytes)
}

func (s *snmParser) MarshalToBytes(matrix *DecisionMatrix) ([]byte, error) {
	return yaml.Marshal(matrix)
}

func (s *snmParser) UnmarshalFromBytes(yamlBytes []byte) (*DecisionMatrix, error) {
	matrix := &DecisionMatrix{}
	if err := yaml.Unmarshal(yamlBytes, matrix); err != nil {
		return nil, err
	}
	return matrix, nil
}
