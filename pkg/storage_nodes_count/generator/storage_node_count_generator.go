package main

import (
	"fmt"
	storagenodescount "github.com/libopenstorage/operator/pkg/storage_nodes_count"
	"math"
)

const k8sMaxNodes = 5000

func main() {
	matrix := storagenodescount.DecisionMatrix{
		Rows: getStorageNodeCountMatrixRows(),
	}

	for _, path := range []string{storagenodescount.SncYamlPath, storagenodescount.TestFilePath} {
		if err := storagenodescount.NewStorageNodesMatrixParser().MarshalToYaml(&matrix, path); err != nil {
			fmt.Println("Failed to generate storage nodes count decision matrix yaml: ", err)
			return
		}
		fmt.Println("Generated storage nodes decision matrix yaml at ", path)
	}
}

type thresh struct {
	nodeCount  uint64
	multiplier float64
}

func getStorageNodeCountMatrixRows() []storagenodescount.MatrixRow {
	var rows []storagenodescount.MatrixRow
	threshold := []thresh{
		{
			nodeCount:  6,
			multiplier: 1.0,
		},
		{
			nodeCount:  20,
			multiplier: 0.6,
		},
		{
			nodeCount:  100,
			multiplier: 0.33,
		},
		{
			nodeCount:  1000,
			multiplier: 0.25,
		},
		{
			// max nodes supported by k8s as of 1.25
			nodeCount:  k8sMaxNodes,
			multiplier: 0.1,
		},
	}
	storageCount := float64(0)
	index := 0
	row := storagenodescount.MatrixRow{
		TotalNodesMin: 1,
	}
	for nodeCount := uint64(1); nodeCount <= k8sMaxNodes; nodeCount++ {
		if index >= len(threshold) {
			break
		}
		if threshold[index].nodeCount < nodeCount {
			index++
		}
		expected := math.Ceil(float64(nodeCount) * threshold[index].multiplier)
		if expected > storageCount {
			row.TotalNodesMax = nodeCount
			row.StorageNodeCount = uint64(expected)
			storageCount = expected
			rows = append(rows, row)
			row = storagenodescount.MatrixRow{
				TotalNodesMin: nodeCount + 1,
			}
		}
	}
	rows[len(rows)-1].TotalNodesMax = k8sMaxNodes
	return rows
}
