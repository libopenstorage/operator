package px_resource_gateway

import (
	"container/list"
	"fmt"
	"sync"

	pb "github.com/libopenstorage/operator/proto"
)

var (
	priorityDescendingOrder = []pb.SemaphoreAccessPriority_Type{
		pb.SemaphoreAccessPriority_HIGH,
		pb.SemaphoreAccessPriority_MEDIUM,
		pb.SemaphoreAccessPriority_LOW,
	}
)

type PriorityQueue interface {
	Enqueue(clientId string, priority pb.SemaphoreAccessPriority_Type) error
	Dequeue(priority pb.SemaphoreAccessPriority_Type) error
	Front() (clientId string, accessPriority pb.SemaphoreAccessPriority_Type)
	Remove(clientId string) error
}

type priorityQueue struct {
	sync.Mutex
	// using list.List for effecicient implementation of queues
	queues       map[pb.SemaphoreAccessPriority_Type]*list.List
	maxQueueSize uint
}

func NewPriorityQueue(maxQueueSize uint) PriorityQueue {
	queues := make(map[pb.SemaphoreAccessPriority_Type]*list.List)
	for _, priority := range priorityDescendingOrder {
		queues[priority] = list.New()
	}
	return &priorityQueue{
		queues:       queues,
		maxQueueSize: maxQueueSize,
	}
}

func (pq *priorityQueue) Enqueue(clientId string, priority pb.SemaphoreAccessPriority_Type) error {
	pq.Lock()
	defer pq.Unlock()

	if pq.lengthWithLock() == pq.maxQueueSize {
		return fmt.Errorf("queue is full")
	}

	pq.queues[priority].PushBack(clientId)
	return nil
}

func (pq *priorityQueue) Dequeue(priority pb.SemaphoreAccessPriority_Type) error {
	pq.Lock()
	defer pq.Unlock()

	if pq.queues[priority].Front() == nil {
		return fmt.Errorf("queue %v is empty", priority)
	}

	pq.queues[priority].Remove(pq.queues[priority].Front())
	return nil
}

func (pq *priorityQueue) Front() (string, pb.SemaphoreAccessPriority_Type) {
	pq.Lock()
	defer pq.Unlock()

	for _, priority := range priorityDescendingOrder {
		if pq.queues[priority].Front() != nil {
			return pq.queues[priority].Front().Value.(string), priority
		}
	}
	return "", pb.SemaphoreAccessPriority_LOW
}

func (pq *priorityQueue) Remove(clientId string) error {
	pq.Lock()
	defer pq.Unlock()

	for _, q := range pq.queues {
		for e := q.Front(); e != nil; e = e.Next() {
			if e.Value.(string) == clientId {
				q.Remove(e)
				return nil
			}
		}
	}
	return fmt.Errorf("client %v not found in the queue", clientId)
}

// lengthWithLock returns the total number of elements across all queues
// and expects the caller to hold the lock
func (pq *priorityQueue) lengthWithLock() uint {
	var length uint
	for _, q := range pq.queues {
		length += uint(q.Len())
	}
	return length
}
