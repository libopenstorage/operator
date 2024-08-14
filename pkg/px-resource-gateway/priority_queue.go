package px_resource_gateway

import (
	"container/list"
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
	Enqueue(clientId string, priority pb.SemaphoreAccessPriority_Type)
	Dequeue(priority pb.SemaphoreAccessPriority_Type)
	Front() (string, pb.SemaphoreAccessPriority_Type)
	Remove(clientId string)
}

type priorityQueue struct {
	sync.Mutex
	// using list.List for effecicient implementation of queues
	queues map[pb.SemaphoreAccessPriority_Type]*list.List
}

func NewPriorityQueue() PriorityQueue {
	queues := make(map[pb.SemaphoreAccessPriority_Type]*list.List)
	for _, priority := range priorityDescendingOrder {
		queues[priority] = list.New()
	}
	return &priorityQueue{
		queues: queues,
	}
}

func (pq *priorityQueue) Enqueue(clientId string, priority pb.SemaphoreAccessPriority_Type) {
	pq.Lock()
	defer pq.Unlock()

	pq.queues[priority].PushBack(clientId)
}

func (pq *priorityQueue) Dequeue(priority pb.SemaphoreAccessPriority_Type) {
	pq.Lock()
	defer pq.Unlock()

	if pq.queues[priority].Front() != nil {
		pq.queues[priority].Remove(pq.queues[priority].Front())
	}
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

func (pq *priorityQueue) Remove(clientId string) {
	pq.Lock()
	defer pq.Unlock()

	for _, q := range pq.queues {
		for e := q.Front(); e != nil; e = e.Next() {
			if e.Value.(string) == clientId {
				q.Remove(e)
				return
			}
		}
	}
}
