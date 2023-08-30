package depresolver

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	IsResolved(obj client.Object, graphNodeName string) bool
	SetResolved(obj client.Object, graphNodeName string)
	Run()
}

type WildCardFunc func(obj client.Object, graphNodeName string) bool

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
func New(adjList string, wildcard WildCardFunc) IGraph {
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

func (g *Graph) IsResolved(obj client.Object, graphNodeName string) bool {
	node, ok := g.graph[graphNodeName]
	if !ok {
		g.graph[graphNodeName] = &Node{
			Dependencies: []string{},
			Resolved:     true,
			ResolvedAt:   time.Now(),
		}
		return true
	}

	for _, depName := range node.Dependencies {
		if depName == wildcardToken {
			return g.wildcard(obj, graphNodeName)
		}
		if !g.IsResolved(obj, graphNodeName) {
			return false
		}
	}

	node.Resolved = true

	return node.Resolved
}

func (g *Graph) SetResolved(obj client.Object, graphNodeName string) {
	node, ok := g.graph[graphNodeName]
	if !ok {
		node = &Node{
			Dependencies: []string{},
		}

		g.graph[graphNodeName] = node
	}
	node.Resolved = true
	node.ResolvedAt = time.Now()
}
