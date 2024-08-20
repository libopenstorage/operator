package px_resource_gateway

import (
	"container/list"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/libopenstorage/openstorage/pkg/sched"
	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	nullLock                    uint32 = 0
	configMapLocksKey                  = "locks"
	configMapNLocksKey                 = "nlocks"
	configMapLockHoldTimeoutKey        = "lockHoldTimeout"
)

type SemaphorePriorityQueue interface {
	AcquireLock(clientId string, priority pb.SemaphoreAccessPriority_Type) (pb.SemaphoreAccessStatus_Type, error)
	ReleaseLock(clientId string) error
	KeepAlive(clientId string) pb.SemaphoreAccessStatus_Type
}

type SemaphoreConfig struct {
	NLocks                uint32
	ConfigMapName         string
	ConfigMapNamespace    string
	ConfigMapLabels       map[string]string
	ConfigMapUpdatePeriod time.Duration
	DeadNodeTimeout       time.Duration
	LockHoldTimeout       time.Duration
	MaxQueueSize          uint
}

// semaphoreLock is a struct to hold the lock information
//
// fields are exported for json marshalling
type semaphoreLock struct {
	Id           uint32
	TimeAcquired time.Time
}

// semaphorePriorityQueue implements the SemaphorePriorityQueue interface
type semaphorePriorityQueue struct {
	priorityQ PriorityQueue

	// locks
	mutex          sync.Mutex
	locks          map[string]semaphoreLock
	availableLocks *list.List

	// heartbeats
	heartbeats map[string]time.Time

	// configmap backend
	configMap           *corev1.ConfigMap
	configMapUpdateDone chan struct{}

	// semaphore config
	config *SemaphoreConfig
}

// CreateSemaphorePriorityQueue creates a new config map in kubernetes
// and initializes a new semaphore priority queue object with it
func CreateSemaphorePriorityQueue(config *SemaphoreConfig) SemaphorePriorityQueue {
	return createSemaphorePriorityQueue(config)
}

func createSemaphorePriorityQueue(config *SemaphoreConfig) *semaphorePriorityQueue {
	configMap := createConfigMap(config)
	return newSemaphorePriorityQueue(config, configMap)
}

// NewSemaphorePriorityQueue takes as input an existing config map and semaphore config
// and initializes a new semaphore priority queue object with it
func NewSemaphorePriorityQueue(config *SemaphoreConfig, configMap *corev1.ConfigMap) *semaphorePriorityQueue {
	return newSemaphorePriorityQueue(config, configMap)
}

// newSemaphorePriorityQueue is an internal method to initialize a new semaphore priority queue object
// with the given config and configmap.
//
// It returns the struct object instead of interface to enable testing private methods.
func newSemaphorePriorityQueue(config *SemaphoreConfig, configMap *corev1.ConfigMap) *semaphorePriorityQueue {
	semPQ := &semaphorePriorityQueue{
		priorityQ:           NewPriorityQueue(config.MaxQueueSize),
		heartbeats:          map[string]time.Time{},
		configMapUpdateDone: make(chan struct{}),
		config:              config,
		configMap:           configMap,
	}

	semPQ.populateConfigFromConfigMap()
	semPQ.populateLocks()
	semPQ.startBackgroundTasks()

	return semPQ
}

// create the configmap if it doesn't exist then, fetch the latest copy of configmap and,
// update semaphore config values (nlocks, lockHoldTimeout)
func createConfigMap(config *SemaphoreConfig) *corev1.ConfigMap {

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.ConfigMapNamespace,
			Labels:    config.ConfigMapLabels,
		},
		Data: map[string]string{
			configMapLocksKey:           "",
			configMapNLocksKey:          fmt.Sprintf("%d", config.NLocks),
			configMapLockHoldTimeoutKey: config.LockHoldTimeout.String(),
		},
	}

	_, err := core.Instance().CreateConfigMap(configMap)
	if err != nil && !k8s_errors.IsAlreadyExists(err) {
		panic(fmt.Sprintf("Failed to create configmap %s in namespace %s: %v", config.ConfigMapName, config.ConfigMapNamespace, err))
	}

	return configMap
}

func getNLocksFromConfigMap(configMap *corev1.ConfigMap) (uint32, error) {
	nLocks, err := strconv.Atoi(configMap.Data[configMapNLocksKey])
	if err != nil {
		return 0, err
	}
	return uint32(nLocks), nil
}

func getLockHoldTimeoutFromConfigMap(configMap *corev1.ConfigMap) (time.Duration, error) {
	lockHoldTimeout, err := time.ParseDuration(configMap.Data[configMapLockHoldTimeoutKey])
	if err != nil {
		return 0, err
	}
	return lockHoldTimeout, nil
}

func getLocksFromConfigMap(configMap *corev1.ConfigMap) (map[string]semaphoreLock, error) {
	locks := map[string]semaphoreLock{}
	configMapLocksValue := configMap.Data[configMapLocksKey]
	if configMapLocksValue == "" {
		return locks, nil
	}
	if err := json.Unmarshal([]byte(configMapLocksValue), &locks); err != nil {
		return nil, err
	}
	return locks, nil
}

// loadConfigMap fetches the latest copy of configmap from kubernetes
// and updates the values in the semaphore config
func (s *semaphorePriorityQueue) populateConfigFromConfigMap() {
	s.config.ConfigMapName = s.configMap.Name
	s.config.ConfigMapNamespace = s.configMap.Namespace
	s.config.ConfigMapLabels = s.configMap.Labels

	nLocks, err := getNLocksFromConfigMap(s.configMap)
	if err != nil {
		panic(err)
	}
	s.config.NLocks = uint32(nLocks)

	lockHoldTimeout, err := getLockHoldTimeoutFromConfigMap(s.configMap)
	if err != nil {
		panic(err)
	}
	s.config.LockHoldTimeout = lockHoldTimeout
}

// populate available locks with all lock ids
func (s *semaphorePriorityQueue) populateLocks() {
	isLocked := map[uint32]bool{}

	locks, err := getLocksFromConfigMap(s.configMap)
	if err != nil {
		panic(err)
	}

	s.locks = locks
	for _, lock := range locks {
		isLocked[lock.Id] = true
	}

	s.availableLocks = list.New()
	for i := nullLock + 1; i <= s.config.NLocks; i++ {
		if _, locked := isLocked[i]; !locked {
			s.availableLocks.PushBack(i)
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
		{"cleanupDeadNodes", s.cleanupDeadNodes, s.config.DeadNodeTimeout},
		{"reclaimExpiredLocks", s.reclaimExpiredLocks, s.config.LockHoldTimeout},
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

// AcquireLock acquires a lock for the client with the given priority
func (s *semaphorePriorityQueue) AcquireLock(clientId string, priority pb.SemaphoreAccessPriority_Type) (pb.SemaphoreAccessStatus_Type, error) {
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Debugf("Received AcquireLock request for node %v", clientId)

	// check if the client already has a lock, if yes return
	_, hasLock := s.locks[clientId]
	if hasLock {
		logrus.Debugf("Already acquired lock for node %v", clientId)
		return pb.SemaphoreAccessStatus_LOCKED, nil
	}

	// check if the client is already in the queue, if not add it
	if _, isQueued := s.heartbeats[clientId]; !isQueued {
		logrus.Debugf("Enqueueing node %v with priority %v", clientId, priority)
		err := s.priorityQ.Enqueue(clientId, priority) // no heartbeat - new node request
		logrus.Debugf("Error: %v", err)
		if err != nil {
			return pb.SemaphoreAccessStatus_TYPE_UNSPECIFIED, err
		}
	}

	// update the heartbeat of the client
	s.heartbeats[clientId] = time.Now()

	// try to acquire the lock
	if hasAcquiredLock := s.tryLock(clientId); hasAcquiredLock {
		logrus.Infof("Acquired lock for node %v", clientId)
		updateDone = true
		return pb.SemaphoreAccessStatus_LOCKED, nil
	}
	return pb.SemaphoreAccessStatus_QUEUED, nil
}

// removeLockedClient removes the client from the heartbeats and locks map
// and adds the lock back to the available locks list
//
// removeLockedClient should be called with mutex locked
func (s *semaphorePriorityQueue) removeLockedClient(clientId string, lock semaphoreLock) {
	delete(s.heartbeats, clientId)
	delete(s.locks, clientId)
	s.availableLocks.PushBack(lock.Id)
}

// ReleaseLock releases the lock held by the client
func (s *semaphorePriorityQueue) ReleaseLock(clientId string) error {
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Debugf("Received ReleaseLock request for node %v", clientId)

	// check if the client has a lock, if not return
	lock, hasLock := s.locks[clientId]
	if !hasLock {
		logrus.Warnf("Did NOT find a lock for the node %v!", clientId)
		return nil
	}

	s.removeLockedClient(clientId, lock)
	updateDone = true

	return nil
}

// KeepAlive updates the heartbeat of the client and returns the status of the client
func (s *semaphorePriorityQueue) KeepAlive(clientId string) pb.SemaphoreAccessStatus_Type {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	logrus.Debugf("Received KeepAlive request for node %v", clientId)
	s.heartbeats[clientId] = time.Now()

	_, hasLock := s.locks[clientId]
	if hasLock {
		return pb.SemaphoreAccessStatus_LOCKED
	}
	return pb.SemaphoreAccessStatus_QUEUED
}

// fetchAvailableLock returns the next available lock id from the available locks list
// if no lock is available then it returns nullLock
//
// fetchAvailableLock should be called with mutex locked
func (s *semaphorePriorityQueue) fetchAvailableLock() uint32 {
	if s.availableLocks.Len() == 0 {
		return nullLock
	}
	nextClientId := s.availableLocks.Front()
	s.availableLocks.Remove(nextClientId)
	return nextClientId.Value.(uint32)
}

// tryLock takes as input a client id and checks if the client is at the front of the queue
// if true, then it checks if there is any lock available and assigns it to the client
//
// tryLock should be called with mutex locked
func (s *semaphorePriorityQueue) tryLock(clientId string) bool {
	// check if the node is at the front of the queue
	nextResouceId, priority := s.priorityQ.Front()
	if nextResouceId == "" {
		panic("Queue is empty")
	}
	if nextResouceId != clientId {
		return false
	}
	logrus.Debugf("Next resource in queue: %v", nextResouceId)

	// check if any lock is available
	lockId := s.fetchAvailableLock()
	if lockId == nullLock {
		return false
	}
	logrus.Debugf("Lock %v is available to take", lockId)

	// remove the client from the queue and assign it the lock
	err := s.priorityQ.Dequeue(priority)
	if err != nil {
		panic(err)
	}
	s.locks[clientId] = semaphoreLock{
		Id:           lockId,
		TimeAcquired: time.Now(),
	}

	return true
}

// updateConfigMap updates the configmap with the latest lock information if required
// and notifies all the goroutines waiting on the configMapUpdateDone channel
//
// updateConfigMap is scheduled as a background task
func (s *semaphorePriorityQueue) updateConfigMap() {

	// isUpdateRequired compares two maps and returns true if they are different
	isUpdateRequired := func(map1, map2 map[string]semaphoreLock) bool {
		if len(map1) != len(map2) {
			return true
		}
		for key1, val1 := range map1 {
			// TODO how are two structs compared
			if val2, ok := map2[key1]; !ok || val1 != val2 {
				return true
			}
		}
		return false
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	logrus.Debugf("Running updateConfigMap background task")

	// fetch the locks from the configmap and compare with the current locks
	// if there is no new update then return
	existingLocks, err := getLocksFromConfigMap(s.configMap)
	if err != nil {
		panic(err)
	}
	if !isUpdateRequired(s.locks, existingLocks) {
		return
	}

	lockInfo := map[string]string{}
	for clientId, lock := range s.locks {
		lockInfo[clientId] = fmt.Sprintf("%d", lock.Id)
	}
	logrus.Infof("Updating configmap: %v", lockInfo)

	configMapLocksValue, err := json.Marshal(s.locks)
	if err != nil {
		panic(err)
	}
	s.configMap.Data[configMapLocksKey] = string(configMapLocksValue)

	s.configMap, err = core.Instance().UpdateConfigMap(s.configMap)
	if err != nil {
		panic(err)
	}

	// notify all the goroutines waiting that the update is done
	close(s.configMapUpdateDone)
	// reset the channel for the next batch of waiters
	s.configMapUpdateDone = make(chan struct{})
}

// cleanupDeadNodes removes the dead nodes from the priority queue
// and releases the locks held by them
//
// A client is considered dead when it has not sent a heartbeat
// for more than DeadNodeTimeout duration
//
// cleanupDeadNodes is scheduled as a background task
func (s *semaphorePriorityQueue) cleanupDeadNodes() {
	deadNodes := []string{}

	s.mutex.Lock()
	defer func() {
		s.mutex.Unlock()
		if len(deadNodes) != 0 {
			<-s.configMapUpdateDone // wait for the next update to complete
		}
	}()
	logrus.Debugf("Running cleanupDeadNodes background task")

	for clientId, lastHeartbeat := range s.heartbeats {
		if time.Since(lastHeartbeat) > s.config.DeadNodeTimeout {
			deadNodes = append(deadNodes, clientId)
		}
	}
	if len(deadNodes) == 0 {
		return
	}

	logrus.Warnf("Cleaning up dead nodes: %v", deadNodes)
	for _, clientId := range deadNodes {
		lock, hasLock := s.locks[clientId]
		if hasLock {
			s.removeLockedClient(clientId, lock)
		} else {
			delete(s.heartbeats, clientId)
			err := s.priorityQ.Remove(clientId)
			if err != nil {
				logrus.Errorf("Failed to remove dead node %v from the queue: %v", clientId, err)
			}
		}
	}
}

// reclaimExpiredLocks releases the locks that have been held
// for more than LockHoldTimeout duration
//
// reclaimExpiredLocks is scheduled as a background task
func (s *semaphorePriorityQueue) reclaimExpiredLocks() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	logrus.Debugf("Running reclaimExpiredLocks background task")

	expiredLocks := map[string]semaphoreLock{}

	for clientId, lock := range s.locks {
		if time.Since(lock.TimeAcquired) > s.config.LockHoldTimeout {
			expiredLocks[clientId] = lock
		}
	}
	if len(expiredLocks) == 0 {
		return
	}

	logrus.Warnf("Reclaiming expired locks: %v", expiredLocks)
	for clientId, lock := range expiredLocks {
		s.removeLockedClient(clientId, lock)
	}
}
