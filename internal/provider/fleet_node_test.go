package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func nodePlan(name string) VMResourceModel {
	plan := baseModel(types.StringUnknown())
	plan.Name = types.StringValue(name)
	plan.Node = types.StringValue("node02")
	plan.VPC = types.StringValue("backend")
	plan.SecurityGroups = groupSet("web")
	plan.VPCIP = types.StringUnknown()
	plan.PostTailscaleScript = types.StringValue("echo up")
	plan.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"role": types.StringValue("web")})
	return plan
}

func TestVMOnNodeLifecycle(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	r := &VMResource{client: c}

	plan := nodePlan("k3s1")
	plan.Autostart = types.BoolValue(true)
	plan.Protected = types.BoolValue(true)
	plan.Notes = types.StringValue("owner: payments")
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(ctx, resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if f.fleetLaunch["node"] != "node02" || f.fleetLaunch["vpc"] != "backend" || f.fleetLaunch["post_tailscale_script"] != "echo up" {
		t.Errorf("the hub was sent %v", f.fleetLaunch)
	}
	if tags, _ := f.fleetLaunch["tags"].(map[string]any); tags["role"] != "web" {
		t.Errorf("tags sent = %v", f.fleetLaunch["tags"])
	}
	for _, c := range f.log() {
		if c == "POST /api/v1/vms" || strings.HasPrefix(c, "GET /api/v1/vms/") || strings.HasPrefix(c, "PUT /api/v1/vms/") {
			t.Errorf("a VM on a node must go through the fleet endpoints, saw %s", c)
		}
	}
	row := f.fleetVMs["node02/k3s1"]
	if row["autostart"] != "enable" || row["protected"] != true || row["notes"] != "owner: payments" {
		t.Errorf("settings must reach the node through the hub: %v", row)
	}
	var m VMResourceModel
	resp.State.Get(ctx, &m)
	if m.Node.ValueString() != "node02" || m.VPCIP.ValueString() != "10.20.0.7" || m.IP.ValueString() != "192.0.2.50" ||
		m.Password.ValueString() != "nodepw" || m.State.ValueString() != "running" || m.Tags.Elements()["role"] == nil {
		t.Errorf("state: node=%v vpc_ip=%v ip=%v pw=%v tags=%v", m.Node, m.VPCIP, m.IP, m.Password, m.Tags)
	}

	// Security groups, tags, notes, protection and autostart change in place, through the hub.
	next := m
	next.SecurityGroups = groupSet("default", "web")
	next.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"env": types.StringValue("test")})
	next.Notes = types.StringValue("owner: platform")
	next.Protected = types.BoolValue(false)
	next.Autostart = types.BoolValue(false)
	ur := resource.UpdateResponse{State: resp.State}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, next), State: resp.State}, &ur)
	if ur.Diagnostics.HasError() {
		t.Fatal(ur.Diagnostics)
	}
	if g := f.cards["k3s1"].Groups; len(g) != 2 {
		t.Errorf("groups = %v", g)
	}
	row = f.fleetVMs["node02/k3s1"]
	if tags, _ := row["tags"].(map[string]any); len(tags) != 1 || tags["env"] != "test" {
		t.Errorf("tags on the node = %v", row["tags"])
	}
	if row["notes"] != "owner: platform" || row["protected"] != false || row["autostart"] != "disable" {
		t.Errorf("settings on the node = %v", row)
	}

	// Changing started stops and starts it through the hub.
	stopped := next
	stopped.Started = types.BoolValue(false)
	ur2 := resource.UpdateResponse{State: ur.State}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, stopped), State: ur.State}, &ur2)
	if ur2.Diagnostics.HasError() || row["state"] != "shutoff" {
		t.Errorf("stop: state=%v %v", row["state"], ur2.Diagnostics)
	}

	// The two things the hub cannot do are refused with a message, not skipped.
	bad := m
	bad.DiskGB = types.Int64Value(m.DiskGB.ValueInt64() + 10)
	ur3 := resource.UpdateResponse{State: ur.State}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, bad), State: ur.State}, &ur3)
	if !ur3.Diagnostics.HasError() || !strings.Contains(ur3.Diagnostics.Errors()[0].Summary(), "disk_gb") {
		t.Errorf("a disk change on a node VM must be refused: %v", ur3.Diagnostics)
	}

	// Read finds it by node, and drops it when the node no longer has it.
	rd := resource.ReadResponse{State: resp.State}
	r.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if rd.Diagnostics.HasError() || rd.State.Raw.IsNull() {
		t.Fatalf("read: %v", rd.Diagnostics)
	}
	saved := f.fleetVMs["node02/k3s1"]
	delete(f.fleetVMs, "node02/k3s1")
	rd = resource.ReadResponse{State: resp.State}
	r.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if !rd.State.Raw.IsNull() {
		t.Error("a VM gone from its node must leave the state")
	}
	f.fleetVMs["node02/k3s1"] = saved

	// Delete takes the protection off, then asks the hub to stop it first.
	saved["protected"] = true
	dstate := m
	dstate.Protected = types.BoolValue(true)
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: asState(t, dstate)}, &dr)
	if dr.Diagnostics.HasError() {
		t.Fatal(dr.Diagnostics)
	}
	if f.fleetDelete != "node02/k3s1?confirm=true&stop=true" {
		t.Errorf("delete = %q", f.fleetDelete)
	}
}

func TestVMOnNodeRetriesVPCNotFound(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	f.vpcMisses = 1
	r := &VMResource{client: c}
	old := vpcRetryWait
	vpcRetryWait = 0
	defer func() { vpcRetryWait = old }()
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(ctx, resource.CreateRequest{Plan: asPlan(t, nodePlan("k3s2"))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("one VPC not found should be retried: %v", resp.Diagnostics)
	}
}

func TestVMOnNodeUnknownNode(t *testing.T) {
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	r := &VMResource{client: c}
	plan := nodePlan("k3s3")
	plan.Node = types.StringValue("nowhere")
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not in the fleet") {
		t.Errorf("an unknown node must fail clearly: %v", resp.Diagnostics)
	}
}

func TestVMOnNodeConfigValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		mod     func(*VMResourceModel)
		wantErr bool
	}{
		"iso":       {func(m *VMResourceModel) { m.ISO = types.StringValue("x.iso") }, true},
		"autostart": {func(m *VMResourceModel) { m.Autostart = types.BoolValue(true) }, false},
		"protected": {func(m *VMResourceModel) { m.Protected = types.BoolValue(true) }, false},
		"notes":     {func(m *VMResourceModel) { m.Notes = types.StringValue("n") }, false},
		"stopped":   {func(m *VMResourceModel) { m.Started = types.BoolValue(false) }, false},
	} {
		m := baseModel(types.StringNull())
		m.Node = types.StringValue("node02")
		tc.mod(&m)
		vr := resource.ValidateConfigResponse{}
		st := asState(t, m)
		(&VMResource{}).ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: st.Schema, Raw: st.Raw}}, &vr)
		if vr.Diagnostics.HasError() != tc.wantErr {
			t.Errorf("%s with node: error=%v, want %v", name, vr.Diagnostics.HasError(), tc.wantErr)
		}
	}
}

// A VM moved to another node outside Terraform must stay in the state, on its new node. Dropping it made the next
// plan create a second VM of the same name on the old node.
func TestReadFollowsAVMThatWasMovedToAnotherNode(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.fleetVMs = map[string]map[string]any{
		"node03/web1": {"node": "node03", "name": "web1", "state": "running", "ip": "192.0.2.50", "vcpus": 2, "memory_mb": 2048,
			"disk_cap_gb": 20.0, "network": "default", "autostart": "disable", "protected": false, "notes": "", "os_id": "ubuntu-24.04"},
	}
	r := &VMResource{client: c}
	state := baseModel(types.StringNull())
	state.Node = types.StringValue("node02")
	var rd resource.ReadResponse
	rd.State = asState(t, state)
	r.Read(ctx, resource.ReadRequest{State: asState(t, state)}, &rd)
	if rd.Diagnostics.HasError() || rd.State.Raw.IsNull() {
		t.Fatalf("a moved VM must stay in the state: %v", rd.Diagnostics)
	}
	var got VMResourceModel
	rd.State.Get(ctx, &got)
	if got.Node.ValueString() != "node03" {
		t.Errorf("node = %v, want node03", got.Node)
	}
	if rd.Diagnostics.WarningsCount() != 1 || !strings.Contains(rd.Diagnostics.Warnings()[0].Detail(), `node = "node03"`) {
		t.Errorf("the person is told to set node: %v", rd.Diagnostics)
	}

	// On two nodes at once is not guessed: it is dropped as before.
	f.fleetVMs["node04/web1"] = f.fleetVMs["node03/web1"]
	rd = resource.ReadResponse{State: asState(t, state)}
	r.Read(ctx, resource.ReadRequest{State: asState(t, state)}, &rd)
	if !rd.State.Raw.IsNull() {
		t.Error("a VM on two other nodes is not guessed")
	}
}
