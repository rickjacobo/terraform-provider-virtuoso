package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// peeringHub extends the fake hub with the four peering endpoints. It answers like the real API: the pair is
// stored and returned sorted by name, a repeat is a 409, the same VPC twice is a 400 and an unknown VPC is a 404.
type peeringHub struct {
	*vpcServer
	peers   map[[2]string]bool
	managed map[string]bool
	deletes int
}

func newPeeringHub(t *testing.T) (*VirtuosoClient, *peeringHub) {
	t.Helper()
	h := &peeringHub{
		vpcServer: &vpcServer{vpcs: map[string]map[string]any{}, groups: map[string][]*fakeGroup{}, cards: map[string]*fakeCard{}, floats: map[string][2]string{}},
		peers:     map[[2]string]bool{},
		managed:   map[string]bool{},
	}
	h.vpcs["app"] = map[string]any{"cidr": "10.0.0.0/16", "mtu": 1450}
	h.vpcs["backend"] = map[string]any{"cidr": "10.1.0.0/16", "mtu": 1450}
	h.vpcs["clash"] = map[string]any{"cidr": "10.0.128.0/17", "mtu": 1450}
	h.vpcs["hubvpc"] = map[string]any{"cidr": "10.9.0.0/16", "mtu": 1450}
	h.managed["hubvpc"] = true
	ts := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(ts.Close)
	return NewVirtuosoClient(ts.URL, "test-key", true), h
}

func sortedPair(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

func (h *peeringHub) record(k [2]string) map[string]any {
	return map[string]any{"vpc_a": k[0], "vpc_b": k[1], "cidr_a": h.vpcs[k[0]]["cidr"], "cidr_b": h.vpcs[k[1]]["cidr"], "created_at": "2026-01-01T00:00:00Z"}
}

func (h *peeringHub) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p != "/api/v1/vpc/peerings" && !strings.HasPrefix(p, "/api/v1/vpc/peerings/") {
		h.vpcServer.serve(w, r)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, r.Method+" "+p)
	switch {
	case p == "/api/v1/vpc/peerings" && r.Method == "GET":
		list := []map[string]any{}
		keys := [][2]string{}
		for k := range h.peers {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
		for _, k := range keys {
			list = append(list, h.record(k))
		}
		reply(w, 200, map[string]any{"peerings": list})
	case p == "/api/v1/vpc/peerings" && r.Method == "POST":
		raw, _ := io.ReadAll(r.Body)
		var body struct{ A, B string }
		var m map[string]string
		json.Unmarshal(raw, &m)
		body.A, body.B = m["vpc_a"], m["vpc_b"]
		_, okA := h.vpcs[body.A]
		_, okB := h.vpcs[body.B]
		switch {
		case body.A == "" || body.B == "":
			fail(w, 400, "Send vpc_a and vpc_b, the names of the two VPCs to peer.")
		case body.A == body.B:
			fail(w, 400, "a VPC cannot be peered with itself")
		case !okA || !okB:
			fail(w, 404, "no such VPC")
		case h.managed[body.A] || h.managed[body.B]:
			fail(w, 409, "this VPC is managed by the hub and cannot be changed here")
		case body.A == "clash" || body.B == "clash":
			fail(w, 400, "app (10.0.0.0/16) and clash (10.0.128.0/17) overlap, so they cannot be peered")
		case h.peers[sortedPair(body.A, body.B)]:
			fail(w, 409, "these VPCs are already peered")
		default:
			k := sortedPair(body.A, body.B)
			h.peers[k] = true
			reply(w, 201, h.record(k))
		}
	default:
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/vpc/peerings/"), "/")
		if len(parts) != 2 {
			fail(w, 404, "not found")
			return
		}
		k := sortedPair(parts[0], parts[1])
		if !h.peers[k] {
			fail(w, 404, "these VPCs are not peered")
			return
		}
		switch r.Method {
		case "GET":
			reply(w, 200, h.record(k))
		case "DELETE":
			h.deletes++
			delete(h.peers, k)
			reply(w, 200, map[string]any{"ok": true})
		}
	}
}

func createPeering(t *testing.T, r *VPCPeeringResource, a, b string) resource.CreateResponse {
	t.Helper()
	sch := schemaOf(r)
	plan := VPCPeeringResourceModel{ID: types.StringUnknown(), VPCA: types.StringValue(a), VPCB: types.StringValue(b),
		CIDRA: types.StringUnknown(), CIDRB: types.StringUnknown()}
	resp := resource.CreateResponse{State: stateFor[VPCPeeringResourceModel](t, sch, nil)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(stateFor(t, sch, &plan))}, &resp)
	return resp
}

// hasRequiresReplace runs the attribute's plan modifiers on a changed value and reports whether any asks for a replace.
func hasRequiresReplace(t *testing.T, attr interface{}) bool {
	t.Helper()
	mods := attr.(interface{ StringPlanModifiers() []planmodifier.String }).StringPlanModifiers()
	for _, m := range mods {
		req := planmodifier.StringRequest{
			State:      tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
			Plan:       tfsdk.Plan{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
			StateValue: types.StringValue("x"), PlanValue: types.StringValue("y"), ConfigValue: types.StringValue("y"),
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		m.PlanModifyString(context.Background(), req, resp)
		if resp.RequiresReplace {
			return true
		}
	}
	return false
}

func TestVPCPeeringSchema(t *testing.T) {
	r := NewVPCPeeringResource()
	sch := schemaOf(r)
	if d := sch.Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatal(d)
	}
	a := sch.Schema.Attributes
	for _, n := range []string{"vpc_a", "vpc_b"} {
		if !a[n].IsRequired() {
			t.Errorf("%s must be required", n)
		}
	}
	for _, n := range []string{"id", "cidr_a", "cidr_b"} {
		if !a[n].IsComputed() || a[n].IsRequired() || a[n].IsOptional() {
			t.Errorf("%s must be computed only", n)
		}
	}
	var meta resource.MetadataResponse
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "virtuoso"}, &meta)
	if meta.TypeName != "virtuoso_vpc_peering" {
		t.Errorf("type name = %s", meta.TypeName)
	}
}

func TestVPCPeeringCreateKeepsConfigOrder(t *testing.T) {
	ctx := context.Background()
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}

	// "backend" sorts after "app", so the API flips the pair; the state must not.
	resp := createPeering(t, r, "backend", "app")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got VPCPeeringResourceModel
	resp.State.Get(ctx, &got)
	if got.ID.ValueString() != "backend/app" || got.VPCA.ValueString() != "backend" || got.VPCB.ValueString() != "app" ||
		got.CIDRA.ValueString() != "10.1.0.0/16" || got.CIDRB.ValueString() != "10.0.0.0/16" {
		t.Errorf("state = %+v", got)
	}
	if !h.peers[[2]string{"app", "backend"}] {
		t.Errorf("server = %v", h.peers)
	}

	// Config order that already sorts.
	h.peers = map[[2]string]bool{}
	resp = createPeering(t, r, "app", "backend")
	resp.State.Get(ctx, &got)
	if got.ID.ValueString() != "app/backend" || got.CIDRA.ValueString() != "10.0.0.0/16" || got.CIDRB.ValueString() != "10.1.0.0/16" {
		t.Errorf("state = %+v", got)
	}
}

func TestVPCPeeringCreateErrors(t *testing.T) {
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	for name, tc := range map[string]struct{ a, b, want string }{
		"unknown":  {"app", "nowhere", "no such VPC"},
		"overlap":  {"app", "clash", "overlap"},
		"managed":  {"app", "hubvpc", "managed by the hub"},
		"backstop": {"app", "app", "cannot be peered with itself"},
	} {
		resp := createPeering(t, r, tc.a, tc.b)
		if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), tc.want) {
			t.Errorf("%s: %v", name, resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Errorf("%s: nothing may be saved when create failed", name)
		}
	}
	if len(h.peers) != 0 {
		t.Errorf("server = %v", h.peers)
	}
	// The same-name backstop never reaches the hub: only the other three attempts posted.
	posts := 0
	for _, l := range h.log() {
		if l == "POST /api/v1/vpc/peerings" {
			posts++
		}
	}
	if posts != 3 {
		t.Errorf("posts = %d, calls %v", posts, h.log())
	}
	if resp := createPeering(t, r, "app", "backend"); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	resp := createPeering(t, r, "backend", "app")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "already peered") {
		t.Errorf("a repeat must show the 409: %v", resp.Diagnostics)
	}
}

func TestVPCPeeringReadSwappedOrder(t *testing.T) {
	ctx := context.Background()
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	resp := createPeering(t, r, "backend", "app")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	rd := resource.ReadResponse{State: resp.State}
	r.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if rd.Diagnostics.HasError() {
		t.Fatal(rd.Diagnostics)
	}
	var got VPCPeeringResourceModel
	rd.State.Get(ctx, &got)
	if got.VPCA.ValueString() != "backend" || got.VPCB.ValueString() != "app" || got.ID.ValueString() != "backend/app" ||
		got.CIDRA.ValueString() != "10.1.0.0/16" || got.CIDRB.ValueString() != "10.0.0.0/16" {
		t.Errorf("read flipped the state: %+v", got)
	}
	_ = h
}

func TestVPCPeeringImportEitherOrder(t *testing.T) {
	ctx := context.Background()
	c, _ := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	if resp := createPeering(t, r, "app", "backend"); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	sch := schemaOf(r)
	for id, want := range map[string][3]string{
		"app/backend": {"10.0.0.0/16", "10.1.0.0/16", "app/backend"},
		"backend/app": {"10.1.0.0/16", "10.0.0.0/16", "backend/app"},
	} {
		resp := resource.ImportStateResponse{State: stateFor[VPCPeeringResourceModel](t, sch, nil)}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", id, resp.Diagnostics)
		}
		var got VPCPeeringResourceModel
		resp.State.Get(ctx, &got)
		a, b, _ := strings.Cut(id, "/")
		if got.VPCA.ValueString() != a || got.VPCB.ValueString() != b || got.CIDRA.ValueString() != want[0] ||
			got.CIDRB.ValueString() != want[1] || got.ID.ValueString() != want[2] {
			t.Errorf("%s: state = %+v", id, got)
		}
	}
	for _, id := range []string{"app", "app/", "/app", "a/b/c", "app/app", "app/ghost"} {
		resp := resource.ImportStateResponse{State: stateFor[VPCPeeringResourceModel](t, sch, nil)}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import %q must fail", id)
		}
	}
}

func TestVPCPeeringDrift(t *testing.T) {
	ctx := context.Background()
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	resp := createPeering(t, r, "app", "backend")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	// The peering was ended in the UI: Read drops it, so the next plan creates it again.
	h.peers = map[[2]string]bool{}
	rd := resource.ReadResponse{State: resp.State}
	r.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if rd.Diagnostics.HasError() || !rd.State.Raw.IsNull() {
		t.Errorf("a missing peering must be removed from state: %v", rd.Diagnostics)
	}
}

func TestVPCPeeringDeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	resp := createPeering(t, r, "backend", "app")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: resp.State}, &dr)
	if dr.Diagnostics.HasError() || len(h.peers) != 0 || h.deletes != 1 {
		t.Fatalf("delete: %v, server %v", dr.Diagnostics, h.peers)
	}
	// Already gone: still a success.
	dr = resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: resp.State}, &dr)
	if dr.Diagnostics.HasError() {
		t.Errorf("a 404 on delete must be success: %v", dr.Diagnostics)
	}
}

func TestVPCPeeringValidation(t *testing.T) {
	ctx := context.Background()
	r := &VPCPeeringResource{}
	sch := schemaOf(r)
	for name, tc := range map[string]struct {
		a, b    types.String
		wantErr bool
	}{
		"same":         {types.StringValue("app"), types.StringValue("app"), true},
		"different":    {types.StringValue("app"), types.StringValue("backend"), false},
		"unknown a":    {types.StringUnknown(), types.StringValue("app"), false},
		"unknown b":    {types.StringValue("app"), types.StringUnknown(), false},
		"both unknown": {types.StringUnknown(), types.StringUnknown(), false},
	} {
		m := VPCPeeringResourceModel{ID: types.StringNull(), VPCA: tc.a, VPCB: tc.b, CIDRA: types.StringNull(), CIDRB: types.StringNull()}
		st := stateFor(t, sch, &m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: st.Schema, Raw: st.Raw}}, &resp)
		if resp.Diagnostics.HasError() != tc.wantErr {
			t.Errorf("%s: %v", name, resp.Diagnostics)
		}
		if tc.wantErr && !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "cannot be peered with itself") {
			t.Errorf("%s: unclear message: %v", name, resp.Diagnostics)
		}
	}
}

// Both names require a replace, so changing vpc_b plans a destroy and a create.
func TestVPCPeeringReplaceWhenVPCBChanges(t *testing.T) {
	ctx := context.Background()
	c, h := newPeeringHub(t)
	r := &VPCPeeringResource{client: c}
	sch := schemaOf(r)
	resp := createPeering(t, r, "app", "backend")
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var cur VPCPeeringResourceModel
	resp.State.Get(ctx, &cur)
	for _, n := range []string{"vpc_a", "vpc_b"} {
		attr := sch.Schema.Attributes[n]
		if !hasRequiresReplace(t, attr) {
			t.Errorf("%s must require replace", n)
		}
	}
	// What the replace does: delete the old one, create the new one.
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: resp.State}, &dr)
	h.vpcs["other"] = map[string]any{"cidr": "10.2.0.0/16", "mtu": 1450}
	resp = createPeering(t, r, "app", "other")
	if dr.Diagnostics.HasError() || resp.Diagnostics.HasError() {
		t.Fatal(dr.Diagnostics, resp.Diagnostics)
	}
	if len(h.peers) != 1 || !h.peers[[2]string{"app", "other"}] {
		t.Errorf("server = %v", h.peers)
	}
}
