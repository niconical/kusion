package operation

import (
	"fmt"

	"kusionstack.io/kusion/pkg/engine/models"

	"kusionstack.io/kusion/pkg/log"
	"kusionstack.io/kusion/pkg/status"
	"kusionstack.io/kusion/pkg/util"
	"kusionstack.io/kusion/pkg/util/json"

	"github.com/hashicorp/terraform/dag"
)

type DeleteResourceParser struct {
	resources models.Resources
}

func NewDeleteResourceParser(resources models.Resources) *DeleteResourceParser {
	return &DeleteResourceParser{resources: resources}
}

func (d *DeleteResourceParser) Parse(graph *dag.AcyclicGraph) (s status.Status) {
	util.CheckNotNil(graph, "graph is nil")
	if len(graph.Vertices()) == 0 {
		log.Infof("no vertices in dag when parsing deleted resources. dag:%s", json.Marshal2String(graph))
		return status.NewErrorStatusWithMsg(status.InvalidArgument, "no vertices in dag when parsing deleted resources")
	}

	manifestGraphMap := make(map[string]interface{})
	for _, v := range graph.Vertices() {
		if rn, ok := v.(*ResourceNode); ok {
			id := rn.Hashcode().(string)
			manifestGraphMap[id] = v
		}
	}

	// diff resources to delete
	resourceIndex := d.resources.Index()
	root, err := graph.Root()
	util.CheckNotError(err, "root get error")

	priorDependsOn := make(map[string][]string)
	for key, v := range resourceIndex {
		for _, dp := range v.DependsOn {
			priorDependsOn[dp] = append(priorDependsOn[dp], key)
		}
	}

	for key, resource := range resourceIndex {
		rn := NewResourceNode(key, resourceIndex[key], Delete)
		rnID := rn.Hashcode().(string)

		if !graph.HasVertex(rn) && manifestGraphMap[rnID] == nil {
			log.Infof("resource:%v not found in models. Mark as delete node", key)
			// we cannot delete this node if any node dependsOn this node
			for _, v := range priorDependsOn[rnID] {
				if manifestGraphMap[v] != nil {
					msg := fmt.Sprintf("%s dependson %s, cannot delete resource %s", v, rnID, rnID)
					return status.NewErrorStatusWithMsg(status.Internal, msg)
				}
			}
			graph.Add(rn)
			graph.Connect(dag.BasicEdge(root, rn))
		}

		// always get the latest vertex in the graph.
		rn = GetVertex(graph, rn).(*ResourceNode)
		s = LinkRefNodes(graph, resource.DependsOn, resourceIndex, rn, Delete, manifestGraphMap)
		if status.IsErr(s) {
			return s
		}
	}
	if err = graph.Validate(); err != nil {
		return status.NewErrorStatusWithMsg(status.IllegalManifest, "Found circle dependency in models."+err.Error())
	}
	graph.TransitiveReduction()
	return s
}
