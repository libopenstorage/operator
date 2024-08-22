// Implements a priority queue with priorities high, medium, and low defined in proto.
// The queue supports the following operations: Enqueue, Dequeue, Front, and Remove.
//
// It is implemented with a separate list for each priority.
// When a client is enqueued, it is added to the list corresponding to its priority.
//
// When the front of the queue is requested,
// the queue will return the client at the front of the highest priority non-empty list.
//
// The queue is configured with a maximum size to prevent unbounded growth.
// When the queue is full, Enqueue will return an error.
package resource_gateway

import (
	"container/list"
	"fmt"
	"sync"

	pb "github.com/libopenstorage/operator/proto"
)

var (
	// priorityDescendingOrder is the order of priorities in descending order.
	// It is used to iterate through the queues in order of priority.
	priorityDescendingOrder = []pb.AccessPriority_Type{
		pb.AccessPriority_HIGH,
		pb.AccessPriority_MEDIUM,
		pb.AccessPriority_LOW,
	}
)

// PriorityQueue is an interface for a priority queue.
type PriorityQueue interface {
	// Enqueue adds a client to the queue with the given priority.
	Enqueue(clientId string, priority pb.AccessPriority_Type) error
	// Dequeue removes the client at the front of the queue with the given priority.
	Dequeue(priority pb.AccessPriority_Type) error
	// Front returns the client at the front of the non-empty queue with the highest priority.
	Front() (clientId string, accessPriority pb.AccessPriority_Type)
	// Remove removes the client from the priority queue.
	Remove(clientId string) error
}

// priorityQueue is an implementation of the PriorityQueue interface.
type priorityQueue struct {
	// sync.Mutex protects Q operations from concurrent access
	sync.Mutex
	// queues for each priority
	// uses list for O(1) implementation of Q operations
	queues map[pb.AccessPriority_Type]*list.List
	// maxQueueSize is the maximum number of elements across all queues
	maxQueueSize uint
}

// NewPriorityQueue creates a new priority queue with the given maximum size
// and returns a PriorityQueue interface.
func NewPriorityQueue(maxQueueSize uint) PriorityQueue {
	queues := make(map[pb.AccessPriority_Type]*list.List)
	for _, priority := range priorityDescendingOrder {
		queues[priority] = list.New()
	}
	return &priorityQueue{
		queues:       queues,
		maxQueueSize: maxQueueSize,
	}
}

// Enqueue adds a client to the queue with the given priority.
//
// It returns an error if the queue is full that is
// total number of elements is equal to maxQueueSize.
func (pq *priorityQueue) Enqueue(clientId string, priority pb.AccessPriority_Type) error {
	pq.Lock()
	defer pq.Unlock()

	if pq.len() == pq.maxQueueSize {
		return fmt.Errorf("queue is full")
	}

	pq.queues[priority].PushBack(clientId)
	return nil
}

// Dequeue removes the client at the front of the queue with the given priority.
func (pq *priorityQueue) Dequeue(priority pb.AccessPriority_Type) error {
	pq.Lock()
	defer pq.Unlock()

	if pq.queues[priority].Front() == nil {
		return fmt.Errorf("queue %v is empty", priority)
	}

	pq.queues[priority].Remove(pq.queues[priority].Front())
	return nil
}

// Front returns the client at the front of the priority queue.
//
// It iterates through the queues in descending order of priority
// and returns the front of the first non-empty queue.
func (pq *priorityQueue) Front() (string, pb.AccessPriority_Type) {
	pq.Lock()
	defer pq.Unlock()

	for _, priority := range priorityDescendingOrder {
		if pq.queues[priority].Front() != nil {
			return pq.queues[priority].Front().Value.(string), priority
		}
	}
	return "", pb.AccessPriority_LOW
}

// Remove removes the client from the priority queue.
//
// It iterates through all queues to find the client and removes it.
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

// len returns the total number of elements across all queues
//
// It iterates through all queues and returns the sum of their lengths.
// It should be called with the mutex locked.
func (pq *priorityQueue) len() uint {
	var length uint
	for _, q := range pq.queues {
		length += uint(q.Len())
	}
	return length
}
