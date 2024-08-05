package server

import (
	"fmt"
	"sync"
	"time"

	pb "github.com/libopenstorage/operator/proto"
	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	K8sNamespace         = "kube-system"
	ConfigMapName        = "px-bringup-configmap"
	NULL_LOCK     uint32 = 0
)

var (
	NLocks                  = 10
	ConfigMapUpdateInterval = time.Second * 1
	DeadNodeTimeout         = time.Second * 5
	labels                  = map[string]string{
		"app": "grpc-server",
	}
)

type SemaphorePriorityQueue interface {
	AcquireLock(clientId string, priority pb.SemaphoreAccessPriority_Type) (pb.SemaphoreAccessStatus_Type, error)
	ReleaseLock(clientId string) error
	KeepAlive(clientId string)
}

type semaphorePriorityQueue struct {
	priorityQ PriorityQueue

	// locks
	mutex          sync.Mutex
	locks          map[string]uint32
	availableLocks []uint32

	// heartbeats
	heartbeats     map[string]time.Time
	heartBeatMutex sync.Mutex

	// configmap backend
	configMap           *corev1.ConfigMap
	configMapUpdateDone chan struct{}
}

func NewSemaphorePriorityQueue() SemaphorePriorityQueue {
	// initialize the semaphore structures
	locks := map[string]uint32{}
	availableLocks := []uint32{}
	heartbeats := map[string]time.Time{}

	// populate available locks with all lock ids
	for i := NULL_LOCK + 1; i <= uint32(NLocks); i++ {
		availableLocks = append(availableLocks, i)
	}

	// create configmap if it doesn't exist
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: K8sNamespace,
			Labels:    labels,
		},
		Data: map[string]string{},
	}
	_, err := core.Instance().CreateConfigMap(configMap)
	if err != nil && !k8s_errors.IsAlreadyExists(err) {
		panic(fmt.Sprintf("Failed to create configmap %s in namespace %s: %v", ConfigMapName, K8sNamespace, err))
	}

	// get the latest copy of configmap
	configMap, err = core.Instance().GetConfigMap(ConfigMapName, K8sNamespace)
	if err != nil {
		panic(fmt.Sprintf("Failed to get configmap %s in namespace %s: %v", ConfigMapName, K8sNamespace, err))
	}

	semPQ := &semaphorePriorityQueue{
		priorityQ:           NewPriorityQueue(),
		locks:               locks,
		availableLocks:      availableLocks,
		heartbeats:          heartbeats,
		configMap:           configMap,
		configMapUpdateDone: make(chan struct{}),
	}

	// start background workers
	go semPQ.configMapUpdateRoutine()
	go semPQ.cleanupDeadNodesRoutine()

	return semPQ
}

func (s *semaphorePriorityQueue) AcquireLock(clientId string, priority pb.SemaphoreAccessPriority_Type) (pb.SemaphoreAccessStatus_Type, error) {
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Infof("Received AcquireLock request for node %v", clientId)

	_, alreadyExists := s.locks[clientId]
	if alreadyExists {
		logrus.Debugf("Already acquired lock for node %v", clientId)
		return pb.SemaphoreAccessStatus_LOCKED, nil
	}

	s.heartBeatMutex.Lock()
	_, alreadyExists = s.heartbeats[clientId]
	s.heartbeats[clientId] = time.Now() // update the heartbeat
	s.heartBeatMutex.Unlock()

	if !alreadyExists {
		logrus.Debugf("Enqueueing node %v", clientId)
		s.priorityQ.Enqueue(clientId, priority) // no heartbeat - new node request
	}

	if lockAcquired := s.tryLock(clientId); lockAcquired {
		logrus.Infof("Acquired lock for node %v", clientId)
		updateDone = true
		return pb.SemaphoreAccessStatus_LOCKED, nil
	}
	return pb.SemaphoreAccessStatus_QUEUED, nil
}

func (s *semaphorePriorityQueue) release(clientId string, lockId uint32) {
	delete(s.locks, clientId)
	s.availableLocks = append(s.availableLocks, lockId)
}

func (s *semaphorePriorityQueue) ReleaseLock(clientId string) error {
	logrus.Infof("Received ReleaseLock request for node %v - acquiring mutex", clientId)
	s.mutex.Lock()
	updateDone := false
	defer func() {
		s.mutex.Unlock()
		if updateDone {
			<-s.configMapUpdateDone
		}
	}()
	logrus.Infof("Received ReleaseLock request for node %v", clientId)

	// release the lock
	lockId, ok := s.locks[clientId]
	if !ok {
		logrus.Warnf("Did NOT find a lock for the node %v!", clientId)
		return nil
	}

	// update internal structures
	s.heartBeatMutex.Lock()
	s.heartBeatMutex.Unlock()
	delete(s.heartbeats, clientId)

	s.release(clientId, lockId)
	updateDone = true

	return nil
}

func (s *semaphorePriorityQueue) KeepAlive(clientId string) {
	s.heartBeatMutex.Lock()
	defer s.heartBeatMutex.Unlock()
	logrus.Debugf("Received KeepAlive request for node %v", clientId)
	s.heartbeats[clientId] = time.Now()
}

func (s *semaphorePriorityQueue) fetchAvailableLock() uint32 {
	if len(s.availableLocks) == 0 {
		return NULL_LOCK
	}
	lockId := s.availableLocks[0]
	s.availableLocks = s.availableLocks[1:]
	return lockId
}

func (s *semaphorePriorityQueue) tryLock(clientId string) bool {
	// check if the node is at the front of the queue
	nextResouceId, priority := s.priorityQ.Front()
	if nextResouceId == "" {
		panic("Queue is empty")
	}
	logrus.Debugf("Next resource in queue: %v", nextResouceId)
	if nextResouceId != clientId {
		return false
	}
	// check if any lock is available
	lockId := s.fetchAvailableLock()
	if lockId == NULL_LOCK {
		return false
	}

	s.locks[clientId] = lockId
	s.priorityQ.Dequeue(priority) // cleanup

	return true
}

func (s *semaphorePriorityQueue) updateConfigMap() error {

	isUpdateRequired := func(map1, map2 map[string]string) bool {
		if len(map1) != len(map2) {
			return true
		}
		for key, value := range map1 {
			if val, ok := map2[key]; !ok || val != value {
				return true
			}
		}
		return false
	}

	data := map[string]string{} // reset the data
	for clientId, lockId := range s.locks {
		data[clientId] = fmt.Sprintf("%d", lockId)
	}

	if !isUpdateRequired(data, s.configMap.Data) {
		return nil
	}

	logrus.Infof("Updating configmap: %v", s.locks)
	configMap, err := core.Instance().UpdateConfigMap(s.configMap)
	if err != nil {
		logrus.Errorf("Failed to update configmap", err)
		return err
	}
	s.configMap = configMap
	return nil
}

func (s *semaphorePriorityQueue) configMapUpdateRoutine() error {
	ticker := time.NewTicker(ConfigMapUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			err := s.updateConfigMap()
			if err != nil {
				panic(err)
			}
			// Signal other goroutines that the work is done
			close(s.configMapUpdateDone)
			s.configMapUpdateDone = make(chan struct{})
		}
	}
}

func (s *semaphorePriorityQueue) cleanupDeadNodes() {
	deadNodes := []string{}
	removedNode := map[string]string{}

	s.mutex.Lock()
	s.heartBeatMutex.Lock()
	defer func() {
		s.heartBeatMutex.Unlock()
		s.mutex.Unlock()
		if len(deadNodes) != 0 {
			<-s.configMapUpdateDone
		}
	}()

	for clientId, lastHeartbeat := range s.heartbeats {
		if time.Since(lastHeartbeat) > DeadNodeTimeout {
			delete(s.heartbeats, clientId) // remove the resource footprint
			deadNodes = append(deadNodes, clientId)
		}
	}
	if len(deadNodes) == 0 {
		return
	}
	logrus.Warnf("Cleaning up dead nodes: %v", deadNodes)

	for _, clientId := range deadNodes {
		if lockId, ok := s.locks[clientId]; ok {
			s.release(clientId, lockId)
			removedNode[clientId] = ""
		}
	}

	for _, clientId := range deadNodes {
		if _, ok := removedNode[clientId]; ok { // already removed
			continue
		}
		s.priorityQ.Remove(clientId)
	}
}

func (s *semaphorePriorityQueue) cleanupDeadNodesRoutine() error {
	ticker := time.NewTicker(DeadNodeTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupDeadNodes()
		}
	}
}
