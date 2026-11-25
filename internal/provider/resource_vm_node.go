package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// withNode returns the resource set to reach VMs through the hub when node is set. The client then sends every
// call about the VM to the node that runs it, so create, read, update and delete share one code path.
func (r *VMResource) withNode(node types.String) *VMResource {
	if node.IsNull() || node.IsUnknown() || node.ValueString() == "" {
		return r
	}
	cp := *r
	cp.client = r.client.onNode(node.ValueString())
	return &cp
}

func hasNode(node types.String) bool {
	return !node.IsNull() && !node.IsUnknown() && node.ValueString() != ""
}

// movedTo is the one other node that has a VM of this name, or "" when there is none or more than one, so it
// is not guessed.
func (r *VMResource) movedTo(state VMResourceModel) string {
	nodes, err := r.client.FleetNodesOf(state.Name.ValueString())
	if err != nil {
		return ""
	}
	var others []string
	for _, n := range nodes {
		if n != state.Node.ValueString() {
			others = append(others, n)
		}
	}
	if len(others) != 1 {
		return ""
	}
	return others[0]
}
