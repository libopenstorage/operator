package utils

import (
	"sort"

	"github.com/libopenstorage/cloudops"
)

// SortByIOPS sorts the storage decision matrix by the IOPS value
func SortByIOPS(rows []cloudops.StorageDecisionMatrixRow) []cloudops.StorageDecisionMatrixRow {
	sort.Slice(rows, func(l, r int) bool {
		return rows[l].IOPS < rows[r].IOPS
	})
	return rows
}

// SortByMinCapacity sorts the storage decision matrix by the MinCapacity value
func SortByMinCapacity(rows []cloudops.StorageDecisionMatrixRow) []cloudops.StorageDecisionMatrixRow {
	sort.Slice(rows, func(l, r int) bool {
		return rows[l].MinSize < rows[r].MinSize
	})
	return rows
}

// SortByPriority sorts the storage decision matrix by the Priority value
func SortByPriority(rows []cloudops.StorageDecisionMatrixRow) []cloudops.StorageDecisionMatrixRow {
	sort.Slice(rows, func(l, r int) bool {
		return rows[l].Priority < rows[r].Priority
	})
	return rows
}

// SortByClosestIOPS sorts the provided rows such that the first element would be
// closest to the requested IOPS
func SortByClosestIOPS(requestedIOPS uint32, rows []cloudops.StorageDecisionMatrixRow) []cloudops.StorageDecisionMatrixRow {
	sort.Slice(rows, func(l, r int) bool {
		diffL := rows[l].IOPS - requestedIOPS
		diffR := rows[r].IOPS - requestedIOPS
		return diffL < diffR
	})
	return rows
}

// CopyDecisionMatrix creates a copy of the decision matrix
func CopyDecisionMatrix(matrix *cloudops.StorageDecisionMatrix) *cloudops.StorageDecisionMatrix {
	matrixCopy := &cloudops.StorageDecisionMatrix{}
	matrixCopy.Rows = make([]cloudops.StorageDecisionMatrixRow, len(matrix.Rows))
	copy(matrixCopy.Rows, matrix.Rows)
	return matrixCopy
}
