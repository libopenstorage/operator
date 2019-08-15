package common

import (
	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/pkg/utils"
	"github.com/sirupsen/logrus"
)

/*
   ====================================
   Storage Distribution Algorithm
   ====================================

  The storage distribution algorithm provides an optimum
  storage distribution strategy for a given set of following inputs:
  - Requested IOPS from cloud storage.
  - Minimum capacity for the whole cluster.
  - Number of zones in the cluster.
  - Number of instances in the cluster.
  - An storage decision matrix.

  Following is the algorithm:
  - Sort the decision matrix by IOPS
  - Filter out the rows which do not meet the requested IOPS
  - Sort the filtered rows by Priority
  - Calculate minCapacityPerZone
  - (loop) For each of the filtered row:
    - Find capacityPerNode = minCapacityPerZone / instancesPerZone
    - (loop) Until capacityPerNode is less than row.MinSize*row.MinDrives
        - Reduce instancesPerZone by 1
        - if instancesPerZone reaches 0 try the next filtered row
        - else recalculate capacityPerNode using the new instancesPerZone
    - capacityPerNode is at optimum level. Found the right candidate
  - Out of both the loops, could not find a candidate.

  TODO:
   - Take into account instance types and their supported drives
   - Take into account max no. of drives that need to be attached on the instance.
   - Take into account the effect on the overall throughput when multiple drives are attached
     on the same instance.
*/

// GetStorageDistribution returns the storage distribution
// for the provided request and decision matrix
func GetStorageDistribution(
	request *cloudops.StorageDistributionRequest,
	decisionMatrix *cloudops.StorageDecisionMatrix,
) (*cloudops.StorageDistributionResponse, error) {
	response := &cloudops.StorageDistributionResponse{}
	for _, userRequest := range request.UserStorageSpec {
		candidate, instancePerZone, driveCapacity, err := getStorageDistributionCandidate(
			decisionMatrix,
			userRequest,
			request.InstancesPerZone,
			request.ZoneCount,
		)
		if err != nil {
			return nil, err
		}
		response.InstanceStorage = append(
			response.InstanceStorage,
			&cloudops.StoragePoolSpec{
				DriveCapacityGiB: driveCapacity,
				DriveType:        candidate.DriveType,
				InstancesPerZone: instancePerZone,
				DriveCount:       candidate.InstanceMinDrives,
				IOPS:             candidate.IOPS,
			},
		)

	}
	return response, nil
}

func getStorageDistributionCandidate(
	decisionMatrix *cloudops.StorageDecisionMatrix,
	request *cloudops.StorageSpec,
	requestedInstancesPerZone int,
	zoneCount int,
) (*cloudops.StorageDecisionMatrixRow, int, uint64, error) {

	logRequest(request, requestedInstancesPerZone, zoneCount)

	dm := utils.CopyDecisionMatrix(decisionMatrix)

	dm.Rows = utils.SortByIOPS(dm.Rows)

	if zoneCount <= 0 {
		return nil, -1, 0, cloudops.ErrNumOfZonesCannotBeZero
	}
	// Calculate min capacity per zone
	minCapacityPerZone := request.MinCapacity / uint64(zoneCount)

	// Filter out rows which have lower IOPS
	var index int
	for index = 0; index < len(dm.Rows); index++ {
		if dm.Rows[index].IOPS >= request.IOPS {
			break
		}
	}
	filteredRows := dm.Rows[index:]
	// Sort the filtered rows by priority
	filteredRows = utils.SortByPriority(filteredRows)

	// Start the with the candidate with highest priority
	for i := 0; i < len(filteredRows); i++ {
		candidateRow := filteredRows[i]
		instancesPerZone := requestedInstancesPerZone

		// The following loop tries to determine the min capacity per node to provision such that
		// the min capacity for the cluster is achieved with maximum distribution of storage nodes
		// in the cluster.
		capacityPerNode := minCapacityPerZone / uint64(instancesPerZone)
		for capacityPerNode < candidateRow.MinSize*uint64(candidateRow.InstanceMinDrives) {
			printCandidates("Candidate", []cloudops.StorageDecisionMatrixRow{candidateRow}, instancesPerZone, capacityPerNode)
			instancesPerZone--
			if instancesPerZone == 0 {
				break
			}
			capacityPerNode = minCapacityPerZone / uint64(instancesPerZone)
		}

		if instancesPerZone == 0 {
			// We could not find a good distribution
			printCandidates("Candidate failed as instances per zone exhausted", []cloudops.StorageDecisionMatrixRow{candidateRow}, instancesPerZone, capacityPerNode)
			continue
		}
		driveCapacity := capacityPerNode / uint64(candidateRow.InstanceMinDrives)
		return &candidateRow, instancesPerZone, driveCapacity, nil
	}
	return nil, -1, 0, cloudops.ErrStorageDistributionCandidateNotFound

}

func printCandidates(
	msg string,
	candidates []cloudops.StorageDecisionMatrixRow,
	instancePerZone int,
	capacityPerNode uint64,
) {
	for _, candidate := range candidates {
		logrus.WithFields(logrus.Fields{
			"IOPS":       candidate.IOPS,
			"MinSize":    candidate.MinSize,
			"DriveType":  candidate.DriveType,
			"Priority":   candidate.Priority,
			"DriveCount": candidate.InstanceMinDrives,
		}).Debugf("%v for %v instances per zone with a total capacity per node %v",
			msg, instancePerZone, capacityPerNode)
	}
}

func logRequest(
	request *cloudops.StorageSpec,
	requestedInstancesPerZone int,
	zoneCount int,
) {
	logrus.WithFields(logrus.Fields{
		"IOPS":             request.IOPS,
		"MinCapacity":      request.MinCapacity,
		"InstancesPerZone": requestedInstancesPerZone,
		"ZoneCount":        zoneCount,
	}).Debugf("-- Storage Distribution Pool Request --")
}
