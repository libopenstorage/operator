package depresolver

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	wildcardToken = "*"
)

type Node struct {
	Resolved     bool
	Dependencies []string
	ResolvedAt   time.Time
}

type IGraph interface {
	IsResolved(nodeName string) bool
	SetResolved(nodeName string)
	Run()
}

type WildCardFunc func(nodeName string) bool

type Graph struct {
	k8sClientset *kubernetes.Clientset
	namespace    string
	lock         *sync.Mutex
	graph        map[string]*Node
	wildcard     WildCardFunc
}

// Takes in yaml representation of the adjList in a json,
// Failure to read the adjList or it's absence would resort to default adjacency list
// The default adjacency list can also be treasted as an example for building your custom tree
func New(endpoints []string, adjList string, wildcard WildCardFunc) IGraph {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("error getting cluster config: %v", err)
	}

	k8sClientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("error getting rest cluster config: %v", err)
	}
	
	g := &Graph{
		k8sClientset: k8sClientset,
		lock:         &sync.Mutex{},
		graph:        map[string]*Node{},
		wildcard:     wildcard,
	}

	g.parse(adjList)

	return g
}

func (g *Graph) parse(adjList string) (err error) {
	if adjList == "" {
		adjList = DefaultDependencies
	}
	adjListMap := map[string][]string{}

	err = json.Unmarshal([]byte(adjList), &adjListMap)
	if nil != err {
		return
	}

	g.graph = make(map[string]*Node)

	for nodeName, nodeAdj := range adjListMap {
		g.graph[nodeName] = &Node{
			Resolved:     false,
			Dependencies: nodeAdj,
		}
	}

	return
}

func (g *Graph) IsResolved(nodeName string) bool {
	node, ok := g.graph[nodeName]
	if !ok {
		g.graph[nodeName] = &Node{
			Dependencies: []string{},
			Resolved:     true,
			ResolvedAt:   time.Now(),
		}
		return true
	}

	for _, depName := range node.Dependencies {
		if depName == wildcardToken {
			return g.wildcard(nodeName)
		}
		if !g.IsResolved(depName) {
			return false
		}
	}

	node.Resolved = true

	return node.Resolved
}

func (g *Graph) SetResolved(nodeName string) {
	node, ok := g.graph[nodeName]
	if !ok {
		node = &Node{
			Dependencies: []string{},
		}

		g.graph[nodeName] = node
	}
	node.Resolved = true
	node.ResolvedAt = time.Now()
}
