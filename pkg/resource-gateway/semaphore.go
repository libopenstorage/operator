// Package resource_gateway provides a semaphore implementation backed by priority queue
package resource_gateway

import (
	"container/list"
	"sync"
	"time"

	"github.com/libopenstorage/openstorage/pkg/sched"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/sirupsen/logrus"
)

const (
	nullPermit uint32 = 0
)

type SemaphorePriorityQueue interface {
	Acquire(clientId string, priority pb.AccessPriority_Type) (pb.AccessStatus_Type, error)
	Release(clientId string) error
	Heartbeat(clientId string) pb.AccessStatus_Type

	Update(config *SemaphoreConfig)
}

// activeLease struct holds the lease Id and the time
// at which the lease was acquired for actives leases
//
// The struct is marshalled to json and stored in the configmap;
// the fields are exported to enable json marshalling
type activeLease struct {
	PermitId     uint32
	TimeAcquired int64
}

// semaphorePriorityQueue implements the SemaphorePriorityQueue interface
type semaphorePriorityQueue struct {
	// configurations
	config *SemaphoreConfig

	// thread safety
	mutex sync.Mutex

	// internal state
	priorityQueue    PriorityQueue
	activeLeases     map[string]activeLease
	availablePermits *list.List
	heartbeats       map[string]int64

	// persistent state
	configMap           *configMap
	configMapUpdateDone chan struct{}
}

// NewSemaphorePriorityQueue creates a new or loads an existing semaphore priority queue
// if the backing configmap does not exist then it creates a new one
func NewSemaphorePriorityQueueWithConfig(config *SemaphoreConfig) *semaphorePriorityQueue {
	// create or update the configmap
	configMap, err := createOrUpdateConfigMap(config)
	if err != nil {
		panic(err)
	}

	semPQ := &semaphorePriorityQueue{
		priorityQueue:       NewPriorityQueue(config.MaxQueueSize),
		heartbeats:          map[string]int64{},
		configMapUpdateDone: make(chan struct{}),
		config:              config,
		configMap:           configMap,
	}

	semPQ.populateSemaphoreConfig()
	semPQ.initPermitsAndLeases()
	semPQ.startBackgroundTasks()

	return semPQ
}

// NewSemaphorePriorityQueue creates a new or loads an existing semaphore priority queue
// if the backing configmap does not exist then it creates a new one
func NewSemaphorePriorityQueueWithConfigMap(config *SemaphoreConfig) *semaphorePriorityQueue {
	// create or update the configmap
	configMap, err := getConfigMap(config)
	if err != nil {
		panic(err)
	}

	semPQ := &semaphorePriorityQueue{
		priorityQueue:       NewPriorityQueue(config.MaxQueueSize),
		heartbeats:          map[string]int64{},
		configMapUpdateDone: make(chan struct{}),
		config:              config,
		configMap:           configMap,
	}

	semPQ.populateSemaphoreConfig()
	semPQ.initPermitsAndLeases()
	semPQ.startBackgroundTasks()

	return semPQ
}

func (s *semaphorePriorityQueue) Update(config *SemaphoreConfig) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	logrus.Infof("Update semaphore config: %v", config)
	s.config = config
	s.configMap.update(config)

	// TODO: update the internal state based on the new config
	s.populateSemaphoreConfig()
	s.initPermitsAndLeases()
}

// populateSemaphoreConfig fetches the latest copy of configmap from kubernetes
// and updates the values in the semaphore config
func (s *semaphorePriorityQueue) populateSemaphoreConfig() {
	s.config.ConfigMapName = s.configMap.Name()
	s.config.ConfigMapNamespace = s.configMap.Namespace()
	s.config.ConfigMapLabels = s.configMap.Labels()

	nPermits, err := s.configMap.NPermits()
	if err != nil {
		panic(err)
	}
	s.config.NPermits = nPermits

	leaseTimeout, err := s.configMap.LeaseTimeout()
	if err != nil {
		panic(err)
	}
	s.config.LeaseTimeout = leaseTimeout
}

// populate structures for active leases and available permits
func (s *semaphorePriorityQueue) initPermitsAndLeases() {
	mapIsPermitGranted := map[uint32]bool{}

	activeLeases, err := s.configMap.ActiveLeases()
	if err != nil {
		panic(err)
	}

	s.activeLeases = activeLeases
	for _, lease := range activeLeases {
		mapIsPermitGranted[lease.PermitId] = true
	}

	s.availablePermits = list.New()
	for i := nullPermit + 1; i <= s.config.NPermits; i++ {
		if _, isGranted := mapIsPermitGranted[i]; !isGranted {
			s.availablePermits.PushBack(i)
		}
	}
}

// start background workers
func (s *semaphorePriorityQueue) startBackgroundTasks() {
	bgTasks := []struct {
		name     string
		f        func()
		interval time.Duration
	}{
		{"updateConfigMap", s.updateConfigMap, s.config.ConfigMapUpdatePeriod},
		{"cleanupDeadClients", s.cleanupDeadClients, s.config.DeadClientTimeout / 2},
		{"reclaimExpiredLeases", s.reclaimExpiredLeases, s.config.LeaseTimeout / 2},
	}

	for _, bgTask := range bgTasks {
		f := bgTask.f
		taskID, err := sched.Instance().Schedule(
			func(_ sched.Interval) { f() },
			sched.Periodic(bgTask.interval),
			time.Now(), false,
		)
		if err != nil {
			panic(err)
		}
		logrus.Debugf("Scheduled task %v with interval %v and Id %v",
			bgTask.name, bgTask.interval, taskID)
	}
}

// Acquire acquires a lease for the client with the given priority
func (s *semaphorePriorityQueue) Acquire(clientId string, priority pb.AccessPriority_Type) (pb.AccessStatus_Type, error) {
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Debugf("Received Acquire request for client %v", clientId)

	// check if the client already has a lease, if yes return
	_, hasLease := s.activeLeases[clientId]
	if hasLease {
		logrus.Debugf("Already acquired lease for client %v", clientId)
		return pb.AccessStatus_LEASED, nil
	}

	// check if the client is already in the queue, if not add it
	// no heartbeat = new client
	if _, isQueued := s.heartbeats[clientId]; !isQueued {
		logrus.Debugf("Enqueueing client %v with priority %v", clientId, priority)
		err := s.priorityQueue.Enqueue(clientId, priority)
		logrus.Debugf("Error: %v", err)
		if err != nil {
			return pb.AccessStatus_TYPE_UNSPECIFIED, err
		}
	}

	// update the heartbeat of the client
	s.heartbeats[clientId] = time.Now().Unix()

	// try to acquire the lease
	if hasAcquiredLease := s.tryAcquire(clientId); hasAcquiredLease {
		logrus.Infof("Acquired lease for client %v", clientId)
		updateDone = true
		return pb.AccessStatus_LEASED, nil
	}
	return pb.AccessStatus_QUEUED, nil
}

// removeClientWithLease removes the client from the heartbeats and activeLeases map
// and adds the respective permit back to the available permits list
//
// removeClientWithLease should be called with mutex locked
func (s *semaphorePriorityQueue) removeClientWithLease(clientId string, lease activeLease) {
	delete(s.heartbeats, clientId)
	delete(s.activeLeases, clientId)
	s.availablePermits.PushBack(lease.PermitId)
}

// Release releases the lease held by the client
func (s *semaphorePriorityQueue) Release(clientId string) error {
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Debugf("Received Release request for client %v", clientId)

	// check if the client has an active lease, if not return
	lease, hasLease := s.activeLeases[clientId]
	if !hasLease {
		logrus.Warnf("Did NOT find an active lease for the client %v!", clientId)
		return nil
	}

	s.removeClientWithLease(clientId, lease)
	updateDone = true

	return nil
}

// Heartbeat updates the heartbeat of the client and returns the status of the client
func (s *semaphorePriorityQueue) Heartbeat(clientId string) pb.AccessStatus_Type {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	logrus.Debugf("Received Heartbeat request for client %v", clientId)
	_, exists := s.heartbeats[clientId]
	if !exists {
		return pb.AccessStatus_TYPE_UNSPECIFIED
	}
	s.heartbeats[clientId] = time.Now().Unix()

	_, hasLease := s.activeLeases[clientId]
	if hasLease {
		return pb.AccessStatus_LEASED
	}
	return pb.AccessStatus_QUEUED
}

// fetchAvailablePermit returns the next available permit id
// from the list of available permits if any else returns nullPermit
//
// fetchAvailablePermit should be called with mutex locked
func (s *semaphorePriorityQueue) fetchAvailablePermit() uint32 {
	if s.availablePermits.Len() == 0 {
		return nullPermit
	}
	nextClientId := s.availablePermits.Front()
	s.availablePermits.Remove(nextClientId)
	return nextClientId.Value.(uint32)
}

// tryAcquire checks if a given client is at the front of the queue and if there is an available permit,
// if true, it assigns the permit to the client
//
// tryAcquire should be called with mutex locked
func (s *semaphorePriorityQueue) tryAcquire(clientId string) bool {
	// check if the client is at the front of the queue
	nextResouceId, priority := s.priorityQueue.Front()
	if nextResouceId == "" {
		panic("Queue is empty")
	}
	if nextResouceId != clientId {
		return false
	}
	logrus.Debugf("Next resource in queue: %v", nextResouceId)

	// check if any permit is available
	permitId := s.fetchAvailablePermit()
	if permitId == nullPermit {
		return false
	}
	logrus.Debugf("Permit %v is available to take", permitId)

	// remove the client from the queue and assign it the permit
	err := s.priorityQueue.Dequeue(priority)
	if err != nil {
		panic(err)
	}
	s.activeLeases[clientId] = activeLease{
		PermitId:     permitId,
		TimeAcquired: time.Now().Unix(),
	}

	return true
}

// updateConfigMap updates the configmap with the active lease data in memory
// and notifies all the goroutines waiting on the configMapUpdateDone channel
//
// updateConfigMap is scheduled as a background task
func (s *semaphorePriorityQueue) updateConfigMap() {

	s.mutex.Lock()
	defer s.mutex.Unlock()
	logrus.Debugf("Running updateConfigMap background task")

	err := s.configMap.UpdateLeases(s.activeLeases)
	if err != nil {
		logrus.Fatalf("Failed to update configmap: %v", err)
	}

	// notify all the goroutines waiting that the update is done
	close(s.configMapUpdateDone)
	// reset the channel for the next batch of waiters
	s.configMapUpdateDone = make(chan struct{})
}

// cleanupDeadClients removes the dead clients from the priority queue
// and releases the leases held by them
//
// A client is considered dead when it has not sent a heartbeat
// for more than DeadClientTimeout duration
//
// cleanupDeadClients is scheduled as a background task
func (s *semaphorePriorityQueue) cleanupDeadClients() {
	deadClients := []string{}

	s.mutex.Lock()
	defer func() {
		s.mutex.Unlock()
		if len(deadClients) != 0 {
			<-s.configMapUpdateDone // wait for the next update to complete
		}
	}()
	logrus.Debugf("Running cleanupDeadClients background task")

	for clientId, lastHeartbeat := range s.heartbeats {
		if time.Since(time.Unix(lastHeartbeat, 0)) > s.config.DeadClientTimeout {
			deadClients = append(deadClients, clientId)
		}
	}
	if len(deadClients) == 0 {
		return
	}

	logrus.Warnf("Cleaning up dead clients: %v", deadClients)
	for _, clientId := range deadClients {
		lease, hasLease := s.activeLeases[clientId]
		if hasLease {
			s.removeClientWithLease(clientId, lease)
		} else {
			delete(s.heartbeats, clientId)
			err := s.priorityQueue.Remove(clientId)
			if err != nil {
				logrus.Errorf("Failed to remove dead client %v from the queue: %v", clientId, err)
			}
		}
	}
}

// reclaimExpiredLeases releases the leases that have been held
// for more than LeaseTimeout duration
//
// reclaimExpiredLeases is scheduled as a background task
func (s *semaphorePriorityQueue) reclaimExpiredLeases() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	logrus.Debugf("Running reclaimExpiredLeases background task")

	expiredLeases := map[string]activeLease{}

	for clientId, lease := range s.activeLeases {
		if time.Since(time.Unix(lease.TimeAcquired, 0)) > s.config.LeaseTimeout {
			expiredLeases[clientId] = lease
		}
	}
	if len(expiredLeases) == 0 {
		return
	}

	logrus.Warnf("Reclaiming expired leases: %v", expiredLeases)
	for clientId, lease := range expiredLeases {
		s.removeClientWithLease(clientId, lease)
	}
}
