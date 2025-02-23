// Graph package provides support for the graph handlers such as supported path
// variables and query params, as well as types for graph processing.
package graph

import (
	"fmt"
	"time"
)

const (
	BlackHoleCluster      string = "BlackHoleCluster"
	GraphTypeApp          string = "app"
	GraphTypeService      string = "service" // Treated as graphType Workload, with service injection, and then condensed
	GraphTypeVersionedApp string = "versionedApp"
	GraphTypeWorkload     string = "workload"
	GraphTypePod          string = "pod"
	NodeTypeAggregate     string = "aggregate" // The special "aggregate" traffic node
	NodeTypeApp           string = "app"
	NodeTypeBox           string = "box" // The special "box" node. isBox will be set to "app" | "cluster" | "namespace"
	NodeTypeService       string = "service"
	NodeTypeUnknown       string = "unknown" // The special "unknown" traffic gen node
	NodeTypeWorkload      string = "workload"
	NodeTypePod           string = "pod"
	PassthroughCluster    string = "PassthroughCluster"
	TF                    string = "2006-01-02 15:04:05" // TF is the TimeFormat for timestamps
	Unknown               string = "unknown"             // Istio unknown label value
)

type Node struct {
	ID        string   // unique identifier for the node
	NodeType  string   // Node type
	Cluster   string   // Cluster
	Namespace string   // Namespace
	Workload  string   // Workload (deployment) name
	App       string   // Workload app label value
	Version   string   // Workload version label value
	Service   string   // Service name
	Pod       string   // Pod name
	Arch      string   // pod arch
	Edges     []*Edge  // child nodes
	Metadata  Metadata // app-specific data
}

type Edge struct {
	Source   *Node
	Dest     *Node
	Metadata Metadata // app-specific data
}

type NamespaceInfo struct {
	Name     string
	Duration time.Duration
	IsIstio  bool
}

type NamespaceInfoMap map[string]NamespaceInfo

func NewNamespaceInfoMap() NamespaceInfoMap {
	return make(map[string]NamespaceInfo)
}

type ServiceName struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// WEInfo provides static information about a workload entry
// associated with a workload node.
type WEInfo struct {
	// Name of the workload entry object
	// required:true
	Name string `json:"name"`
}

// SEInfo provides static information about the service entry
type SEInfo struct {
	Hosts     []string `json:"hosts"`     // configured list of hosts
	Location  string   `json:"location"`  // e.g. MESH_EXTERNAL, MESH_INTERNAL
	Namespace string   `json:"namespace"` // the definition namespace
}

func (s *ServiceName) Key() string {
	return fmt.Sprintf("%s %s %s", s.Cluster, s.Namespace, s.Name)
}

// TrafficMap is a map of app Nodes, each optionally holding Edge data. Metadata
// is a general purpose map for holding any desired node or edge information.
// Each app node should have a unique namespace+workload.  Note that it is feasible
// but likely unusual to have two nodes with the same name+version in the same
// namespace.
type TrafficMap map[string]*Node

// NewNode constructor
func NewNode(cluster, serviceNamespace, service, workloadNamespace, workload, app, version, graphType, pod string) Node {
	id, nodeType := Id(cluster, serviceNamespace, service, workloadNamespace, workload, app, version, graphType, pod)
	namespace := workloadNamespace
	if !IsOK(namespace) {
		namespace = serviceNamespace
	}

	return NewNodeExplicit(id, cluster, namespace, workload, app, version, service, nodeType, graphType, pod)
}

// NewNodeExplicit constructor assigns the specified ID
func NewNodeExplicit(id, cluster, namespace, workload, app, version, service, nodeType, graphType, pod string) Node {
	metadata := make(Metadata)

	// trim unnecessary fields
	switch nodeType {
	case NodeTypeApp:
		// note: we keep workload for a versioned app node because app+version labeling
		// should be backed by a single workload and it can be useful to use the workload
		// name as opposed to the label values.
		if graphType != GraphTypeVersionedApp {
			workload = ""
			version = ""
		}
		service = ""
	case NodeTypeService:
		app = ""
		workload = ""
		version = ""

		if service == PassthroughCluster || service == BlackHoleCluster {
			metadata[IsEgressCluster] = true
		}
	case NodeTypeWorkload:
		// maintain the app+version labeling if it is set, it can be useful
		// for identifying destination rules, providing links, and grouping
		if app == Unknown {
			app = ""
		}
		if version == Unknown {
			version = ""
		}
		service = ""
	case NodeTypePod:
		service = ""
		if graphType == GraphTypePod {
			if namespace != "ingress-nginx" {
				workload = ""
			}
			version = ""
		}
	}

	return Node{
		ID:        id,
		NodeType:  nodeType,
		Cluster:   cluster,
		Namespace: namespace,
		Workload:  workload,
		App:       app,
		Version:   version,
		Service:   service,
		Pod:       pod,
		Edges:     []*Edge{},
		Metadata:  metadata,
	}
}

// AddEdge adds an edge to the specified dest node
func (s *Node) AddEdge(dest *Node) *Edge {
	e := NewEdge(s, dest)
	s.Edges = append(s.Edges, &e)
	return &e
}

// NewEdge constructor
func NewEdge(source, dest *Node) Edge {
	return Edge{
		Source:   source,
		Dest:     dest,
		Metadata: NewMetadata(),
	}
}

// NewTrafficMap constructor
func NewTrafficMap() TrafficMap {
	return make(map[string]*Node)
}

// Id returns the unique node ID
func Id(cluster, serviceNamespace, service, workloadNamespace, workload, app, version, graphType, pod string) (id, nodeType string) {
	// prefer the workload namespace
	namespace := workloadNamespace
	if !IsOK(namespace) {
		namespace = serviceNamespace
	}

	// first, check for the special-case "unknown" source node
	if Unknown == namespace && Unknown == workload && Unknown == app && service == "" {
		return fmt.Sprintf("%s_unknown_source", cluster), NodeTypeUnknown
	}

	// It is possible that a request is made for an unknown destination. For example, an Ingress
	// request to an unknown path. In this case the namespace may or may not be unknown.
	// Every other field is unknown. Allow one unknown service per namespace to help reflect these
	// bad destinations in the graph,  it may help diagnose a problem.
	if Unknown == workload && Unknown == app && Unknown == service {
		return fmt.Sprintf("svc_%s_%s_unknown", cluster, namespace), NodeTypeService
	}

	workloadOk := IsOK(workload)
	appOk := IsOK(app)
	serviceOk := IsOK(service)
	podOk := IsOK(pod)
	if graphType == GraphTypePod {
		if podOk {
			return fmt.Sprintf("pod_%s_%s_%v", cluster, namespace, pod), NodeTypePod
		} else if workloadNamespace == "ingress-nginx" {
			if workloadOk {
				return fmt.Sprintf("vapp_%s_%s_%s", cluster, namespace, workload), NodeTypeApp
			}
		} else {
			return fmt.Sprintf("svc_%s_%s_%s", cluster, namespace, service), NodeTypeService
		}
	}

	if !workloadOk && !appOk && !serviceOk {
		panic(fmt.Sprintf("Failed ID gen1: cluster=[%s] namespace=[%s] workload=[%s] app=[%s] version=[%s] service=[%s] graphType=[%s]", cluster, namespace, workload, app, version, service, graphType))
	}

	// handle workload graph nodes (service graphs are initially processed as workload graphs)
	if graphType == GraphTypeWorkload || graphType == GraphTypeService {
		// workload graph nodes are type workload or service
		if !workloadOk && !serviceOk {
			panic(fmt.Sprintf("Failed ID gen2: cluster=[%s] namespace=[%s] workload=[%s] app=[%s] version=[%s] service=[%s] graphType=[%s]", cluster, namespace, workload, app, version, service, graphType))
		}
		if !workloadOk {
			return fmt.Sprintf("svc_%s_%s_%s", cluster, namespace, service), NodeTypeService
		}
		return fmt.Sprintf("wl_%s_%s_%v", cluster, namespace, workload), NodeTypeWorkload
	}

	// handle app and versionedApp graphs
	versionOk := IsOKVersion(version)
	if appOk {
		// For a versionedApp graph use workload as the Id, if available. It allows us some protection
		// against labeling anti-patterns. It won't be there in a few cases like:
		//   - root node of a node graph
		//   - app box node
		// Otherwise use what we have and alter node type as necessary
		// For a [versionless] App graph use the app label to aggregate versions/workloads into one node
		if graphType == GraphTypeVersionedApp {
			if workloadOk {
				return fmt.Sprintf("vapp_%s_%s_%s", cluster, namespace, workload), NodeTypeApp
			}
			if versionOk {
				return fmt.Sprintf("vapp_%s_%s_%s_%s", cluster, namespace, app, version), NodeTypeApp
			}
		}
		return fmt.Sprintf("app_%s_%s_%s", cluster, namespace, app), NodeTypeApp
	}

	// fall back to workload if applicable
	if workloadOk {
		return fmt.Sprintf("wl_%s_%s_%s", cluster, namespace, workload), NodeTypeWorkload
	}

	// fall back to service as a last resort in the app graph
	return fmt.Sprintf("svc_%s_%s_%s", cluster, namespace, service), NodeTypeService
}

// NewAggregateNode constructor, set svcName and app to "" when not service-specific aggregate
func NewAggregateNode(cluster, namespace, aggregate, aggregateValue, svcName, app string) Node {
	id := AggregateID(cluster, namespace, aggregate, aggregateValue, svcName)

	return NewAggregateNodeExplicit(id, cluster, namespace, aggregate, aggregateValue, svcName, app)
}

// NewAggregateNodeExplicit constructor assigns the specified ID, , set svcName and app to ""
// when not service-specific aggregate
func NewAggregateNodeExplicit(id, cluster, namespace, aggregate, aggregateValue, svcName, app string) Node {
	metadata := make(Metadata)
	metadata[Aggregate] = aggregate
	metadata[AggregateValue] = aggregateValue

	return Node{
		ID:        id,
		NodeType:  NodeTypeAggregate,
		Cluster:   cluster,
		Namespace: namespace,
		Workload:  "",
		App:       app,
		Version:   "",
		Service:   svcName,
		Edges:     []*Edge{},
		Metadata:  metadata,
	}
}

// AggregateID returns the unique node ID
func AggregateID(cluster, namespace, aggregate, aggregateVal, svcName string) (id string) {
	if svcName == "" {
		return fmt.Sprintf("agg_%s_%s_%s_%s", cluster, namespace, aggregate, aggregateVal)
	}
	return fmt.Sprintf("agg_%s_%s_%s_%s_%s", cluster, namespace, aggregate, aggregateVal, svcName)
}
