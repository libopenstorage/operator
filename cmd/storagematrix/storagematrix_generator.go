package main

import (
	"fmt"
	"github.com/libopenstorage/operator/pkg/storagematrix"
	"math"
)

const k8sMaxNodes = 5000

func main() {
	matrix := storagematrix.DecisionMatrix{
		Rows: getStorageNodeCountMatrixRows(),
	}

	if err := storagematrix.NewStorageNodesMatrixParser().MarshalToYaml(&matrix, storagematrix.GeneratorTestFilePath); err != nil {
		fmt.Println("Failed to generate storage nodes count decision matrix yaml: ", err)
		return
	}
	fmt.Println("Generated storage nodes decision matrix yaml at ", storagematrix.GeneratorTestFilePath)
}

type thresh struct {
	nodeCount  uint64
	multiplier float64
}

func getStorageNodeCountMatrixRows() []storagematrix.MatrixRow {
	var rows []storagematrix.MatrixRow
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
	row := storagematrix.MatrixRow{
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
			row = storagematrix.MatrixRow{
				TotalNodesMin: nodeCount + 1,
			}
		}
	}
	rows[len(rows)-1].TotalNodesMax = k8sMaxNodes
	return rows
}
