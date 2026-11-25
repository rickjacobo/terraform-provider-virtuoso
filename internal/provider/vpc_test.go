package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// vpcServer is a small stand in for the VPC part of the virtuOSo REST API. It keeps state and records
// every request, so a test can check what Terraform sent.
type vpcServer struct {
	mu     sync.Mutex
	vpcs   map[string]map[string]any // name -> {cidr, mtu}
	groups map[string][]*fakeGroup   // vpc -> groups
	cards  map[string]*fakeCard      // vm -> card
	floats map[string][2]string      // address -> {vpc, vm}
	nextID int64
	calls  []string
	launch map[string]any

	// fleet: VMs on nodes, and the last delete's query
	fleetVMs    map[string]map[string]any // "node/name" -> row
	fleetLaunch map[string]any
	fleetDelete string
	vpcMisses   int // the next launches answer "VPC not found" this many times
}

type fakeGroup struct {
	Name, Description string
	Rules             []map[string]any
}

type fakeCard struct {
	VPC, IP string
	Groups  []string
}

func newVPCServer(t *testing.T) (*VirtuosoClient, *vpcServer) {
	t.Helper()
	f := &vpcServer{vpcs: map[string]map[string]any{}, groups: map[string][]*fakeGroup{}, cards: map[string]*fakeCard{}, floats: map[string][2]string{}}
	ts := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(ts.Close)
	return NewVirtuosoClient(ts.URL, "test-key", true), f
}

func (f *vpcServer) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func reply(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func fail(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]any{"error": msg, "status": code})
}

func (f *vpcServer) vpcView(name string) map[string]any {
	out := map[string]any{"name": name, "cidr": f.vpcs[name]["cidr"], "mtu": f.vpcs[name]["mtu"], "created_at": "2026-01-01T00:00:00Z"}
	vms := []map[string]any{}
	for vm, c := range f.cards {
		if c.VPC == name {
			vms = append(vms, map[string]any{"vm": vm, "ip": c.IP, "mac": "52:54:00:00:00:01", "server": "lab"})
		}
	}
	out["vms"] = vms
	return out
}

func (f *vpcServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	p := r.URL.Path
	f.calls = append(f.calls, r.Method+" "+p)

	switch {
	case r.Method == "POST" && p == "/api/v1/fleet/vms":
		f.fleetLaunch = body
		node, _ := body["node"].(string)
		if node != "node02" && node != "node03" {
			fail(w, 404, fmt.Sprintf("server %q is not in the fleet", node))
			return
		}
		if f.vpcMisses > 0 {
			f.vpcMisses--
			fail(w, 400, "VPC not found")
			return
		}
		name := body["name"].(string)
		if f.fleetVMs == nil {
			f.fleetVMs = map[string]map[string]any{}
		}
		f.fleetVMs[node+"/"+name] = map[string]any{"node": node, "name": name, "state": "running", "ip": "192.0.2.50",
			"vcpus": 2, "memory_mb": 2048, "disk_cap_gb": 20.0, "network": "vpc:x", "tags": body["tags"],
			"autostart": "disable", "protected": false, "notes": "", "os_id": "ubuntu-24.04"}
		if v, _ := body["vpc"].(string); v != "" {
			if _, ok := f.vpcs[v]; !ok {
				fail(w, 400, "VPC not found")
				return
			}
			groups := []string{"default"}
			if g, ok := body["security_groups"].([]any); ok && len(g) > 0 {
				groups = nil
				for _, x := range g {
					groups = append(groups, x.(string))
				}
			}
			f.cards[name] = &fakeCard{VPC: v, IP: "10.20.0.7", Groups: groups}
		}
		reply(w, 202, map[string]any{"node": node, "local": false, "status": 202, "result": map[string]any{"name": name, "password": "nodepw"}})
		return
	case r.Method == "GET" && p == "/api/v1/fleet/vms":
		var rows []map[string]any
		for _, row := range f.fleetVMs {
			rows = append(rows, map[string]any{"node": row["node"], "name": row["name"], "provider": "On-Prem"})
		}
		reply(w, 200, map[string]any{"vms": rows, "count": len(rows)})
		return
	case strings.HasPrefix(p, "/api/v1/fleet/vms/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/v1/fleet/vms/"), "/")
		key := parts[0] + "/" + parts[1]
		row, ok := f.fleetVMs[key]
		if !ok {
			fail(w, 404, "VM not found")
			return
		}
		switch {
		case r.Method == "DELETE" && len(parts) == 2:
			f.fleetDelete = key + "?" + r.URL.RawQuery
			if row["protected"] == true {
				fail(w, 403, "VM is protected")
				return
			}
			delete(f.fleetVMs, key)
			delete(f.cards, parts[1])
			reply(w, 200, map[string]any{"ok": true})
		case r.Method == "GET" && len(parts) == 2:
			reply(w, 200, row)
		case len(parts) == 3 && parts[2] == "start":
			row["state"] = "running"
			reply(w, 200, map[string]any{"ok": true})
		case len(parts) == 3 && parts[2] == "stop":
			row["state"] = "shutoff"
			reply(w, 200, map[string]any{"ok": true})
		case len(parts) == 3 && parts[2] == "notes":
			row["notes"] = body["notes"]
			reply(w, 200, map[string]any{"ok": true})
		case len(parts) == 3 && parts[2] == "protected":
			row["protected"] = body["protected"]
			reply(w, 200, map[string]any{"ok": true})
		case len(parts) == 3 && parts[2] == "autostart":
			if body["enabled"] == true {
				row["autostart"] = "enable"
			} else {
				row["autostart"] = "disable"
			}
			reply(w, 200, map[string]any{"ok": true})
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "tags":
			tags, _ := row["tags"].(map[string]any)
			if tags == nil {
				tags = map[string]any{}
			}
			for k, v := range body["tags"].(map[string]any) {
				tags[k] = v
			}
			row["tags"] = tags
			reply(w, 200, map[string]any{"ok": true})
		case r.Method == "DELETE" && len(parts) == 4 && parts[2] == "tags":
			tags, _ := row["tags"].(map[string]any)
			delete(tags, parts[3])
			reply(w, 200, map[string]any{"ok": true})
		default:
			fail(w, 404, "no such fleet call")
		}
		return
	case r.Method == "POST" && p == "/api/v1/vpcs":
		name, _ := body["name"].(string)
		if _, ok := f.vpcs[name]; ok {
			fail(w, 409, "a VPC with this name already exists")
			return
		}
		mtu := body["mtu"]
		if mtu == nil {
			mtu = 1450
		}
		f.vpcs[name] = map[string]any{"cidr": body["cidr"], "mtu": mtu}
		reply(w, 201, f.vpcView(name))
		return
	case r.Method == "GET" && p == "/api/v1/vpcs":
		list := []map[string]any{}
		for n := range f.vpcs {
			list = append(list, f.vpcView(n))
		}
		reply(w, 200, map[string]any{"vpcs": list})
		return
	case r.Method == "POST" && p == "/api/v1/vms":
		f.launch = body
		if v, _ := body["vpc"].(string); v != "" {
			if _, ok := f.vpcs[v]; !ok {
				fail(w, 400, "no VPC named "+v)
				return
			}
			groups := []string{"default"}
			if g, ok := body["security_groups"].([]any); ok && len(g) > 0 {
				groups = nil
				for _, x := range g {
					groups = append(groups, x.(string))
				}
			}
			f.cards[body["name"].(string)] = &fakeCard{VPC: v, IP: "10.20.0.5", Groups: groups}
		}
		reply(w, 202, map[string]any{"name": body["name"], "password": "pw", "status": "creating"})
		return
	case r.Method == "GET" && strings.HasPrefix(p, "/api/v1/vms/"):
		name := strings.TrimPrefix(p, "/api/v1/vms/")
		reply(w, 200, map[string]any{"name": name, "state": "running", "ip": "10.20.0.5", "vcpus": 1, "memory_mb": 1024,
			"autostart": "disable", "network": "vpc:x", "os_id": "ubuntu-24.04", "disk_cap_gb": 20.0})
		return
	case r.Method == "GET" && p == "/api/v1/vpc/floating-ips":
		list := []map[string]any{}
		for a, t := range f.floats {
			e := map[string]any{"address": a}
			if t[1] != "" {
				e["vpc"], e["vm"] = t[0], t[1]
			}
			list = append(list, e)
		}
		reply(w, 200, map[string]any{"floating_ips": list, "pool": "192.0.2.200-192.0.2.220"})
		return
	case r.Method == "POST" && p == "/api/v1/vpc/floating-ips":
		addr, _ := body["address"].(string)
		if addr == "" {
			addr = fmt.Sprintf("192.0.2.%d", 200+len(f.floats))
		}
		f.floats[addr] = [2]string{}
		reply(w, 201, map[string]any{"address": addr})
		return
	case strings.HasPrefix(p, "/api/v1/vpc/floating-ips/"):
		rest := strings.TrimPrefix(p, "/api/v1/vpc/floating-ips/")
		addr, action, _ := strings.Cut(rest, "/")
		cur, ok := f.floats[addr]
		if !ok {
			fail(w, 404, "that address is not allocated")
			return
		}
		switch {
		case r.Method == "DELETE":
			if cur[1] != "" {
				fail(w, 409, "detach it first")
				return
			}
			delete(f.floats, addr)
			w.WriteHeader(http.StatusNoContent) // like the real server: no body
		case action == "attach":
			if body["vm"] == "ghost" {
				fail(w, 400, "no VM named ghost in this VPC")
				return
			}
			if cur[1] != "" {
				fail(w, 409, "already attached")
				return
			}
			f.floats[addr] = [2]string{body["vpc"].(string), body["vm"].(string)}
			reply(w, 200, map[string]any{"address": addr, "vpc": body["vpc"], "vm": body["vm"]})
		case action == "detach":
			f.floats[addr] = [2]string{}
			reply(w, 200, map[string]any{"address": addr})
		}
		return
	}

	// /api/v1/vpcs/{name}...
	if !strings.HasPrefix(p, "/api/v1/vpcs/") {
		fail(w, 404, "not found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(p, "/api/v1/vpcs/"), "/")
	vpcName := parts[0]
	if _, ok := f.vpcs[vpcName]; !ok {
		fail(w, 404, "no such VPC")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		reply(w, 200, f.vpcView(vpcName))
	case len(parts) == 1 && r.Method == "DELETE":
		for _, c := range f.cards {
			if c.VPC == vpcName {
				fail(w, 409, "the VPC still has VMs")
				return
			}
		}
		delete(f.vpcs, vpcName)
		reply(w, 200, map[string]any{"ok": true})
	case len(parts) == 2 && parts[1] == "security-groups" && r.Method == "POST":
		g := &fakeGroup{Name: body["name"].(string)}
		g.Description, _ = body["description"].(string)
		f.groups[vpcName] = append(f.groups[vpcName], g)
		reply(w, 201, map[string]any{"name": g.Name, "description": g.Description, "rules": []any{}, "vms": []any{}})
	case len(parts) >= 3 && parts[1] == "security-groups":
		var g *fakeGroup
		for _, x := range f.groups[vpcName] {
			if x.Name == parts[2] {
				g = x
			}
		}
		if g == nil {
			fail(w, 404, "no such group")
			return
		}
		switch {
		case len(parts) == 3 && r.Method == "GET":
			rules := g.Rules
			if rules == nil {
				rules = []map[string]any{}
			}
			reply(w, 200, map[string]any{"name": g.Name, "description": g.Description, "rules": rules, "vms": []any{}})
		case len(parts) == 3 && r.Method == "DELETE":
			out := f.groups[vpcName][:0]
			for _, x := range f.groups[vpcName] {
				if x != g {
					out = append(out, x)
				}
			}
			f.groups[vpcName] = out
			reply(w, 200, map[string]any{"ok": true})
		case len(parts) == 4 && parts[3] == "rules" && r.Method == "POST":
			f.nextID++
			rule := map[string]any{"id": f.nextID, "direction": body["direction"], "protocol": body["protocol"]}
			if rule["protocol"] == "tcp" || rule["protocol"] == "udp" {
				from, to := body["port_from"], body["port_to"]
				if from == nil && to == nil {
					from, to = float64(1), float64(65535)
				} else if to == nil {
					to = from
				}
				rule["port_from"], rule["port_to"] = from, to
			}
			for _, k := range []string{"cidr", "group", "description"} {
				if v, ok := body[k]; ok {
					rule[k] = v
				}
			}
			g.Rules = append(g.Rules, rule)
			reply(w, 201, rule)
		case len(parts) == 5 && parts[3] == "rules" && r.Method == "DELETE":
			id, _ := strconv.ParseInt(parts[4], 10, 64)
			out := g.Rules[:0]
			for _, x := range g.Rules {
				if int64(x["id"].(int64)) != id {
					out = append(out, x)
				}
			}
			g.Rules = out
			reply(w, 200, map[string]any{"ok": true})
		default:
			fail(w, 404, "not found")
		}
	case len(parts) == 4 && parts[1] == "vms" && parts[3] == "security-groups":
		c := f.cards[parts[2]]
		if c == nil || c.VPC != vpcName {
			fail(w, 404, "that VM is not in this VPC")
			return
		}
		if r.Method == "PUT" {
			c.Groups = nil
			for _, x := range body["security_groups"].([]any) {
				c.Groups = append(c.Groups, x.(string))
			}
		}
		reply(w, 200, map[string]any{"vm": parts[2], "security_groups": c.Groups})
	default:
		fail(w, 404, "not found")
	}
}

// ---- helpers for the small resources ----

func stateFor[T any](t *testing.T, sch resource.SchemaResponse, m *T) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	st := tfsdk.State{Schema: sch.Schema, Raw: tftypes.NewValue(sch.Schema.Type().TerraformType(ctx), nil)}
	if m != nil {
		if d := st.Set(ctx, m); d.HasError() {
			t.Fatal(d)
		}
	}
	return st
}

func schemaOf(r resource.Resource) resource.SchemaResponse {
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp
}

func planOf(st tfsdk.State) tfsdk.Plan { return tfsdk.Plan{Schema: st.Schema, Raw: st.Raw} }

func TestVPCResourceSchemas(t *testing.T) {
	for name, r := range map[string]resource.Resource{
		"vpc": NewVPCResource(), "security_group": NewSecurityGroupResource(),
		"security_group_rule": NewSecurityGroupRuleResource(), "floating_ip": NewFloatingIPResource(),
	} {
		if d := schemaOf(r).Schema.ValidateImplementation(context.Background()); d.HasError() {
			t.Errorf("%s: %v", name, d)
		}
	}
	if d := vmSchema(t).Schema.ValidateImplementation(context.Background()); d.HasError() {
		t.Fatal(d)
	}
	a := vmSchema(t).Schema.Attributes
	if g := a["security_groups"]; !g.IsOptional() || !g.IsComputed() {
		t.Error("security_groups must be optional and computed, so leaving it out keeps the server's groups")
	}
	if v := a["vpc"]; !v.IsOptional() || v.IsComputed() {
		t.Error("vpc must be optional")
	}
	if !a["vpc_ip"].IsComputed() {
		t.Error("vpc_ip must be computed")
	}
}

func TestVPCLifecycle(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	r := &VPCResource{client: c}
	sch := schemaOf(r)
	plan := VPCResourceModel{ID: types.StringUnknown(), Name: types.StringValue("backend"), CIDR: types.StringValue("10.20.0.0/16"),
		MTU: types.Int64Unknown(), CreatedAt: types.StringUnknown()}
	resp := resource.CreateResponse{State: stateFor[VPCResourceModel](t, sch, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, sch, &plan))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got VPCResourceModel
	resp.State.Get(ctx, &got)
	if got.ID.ValueString() != "backend" || got.CIDR.ValueString() != "10.20.0.0/16" || got.MTU.ValueInt64() != 1450 || got.CreatedAt.IsUnknown() {
		t.Errorf("state = %+v", got)
	}
	// A VPC that was removed outside Terraform leaves the state.
	delete(f.vpcs, "backend")
	rr := resource.ReadResponse{State: resp.State}
	r.Read(ctx, resource.ReadRequest{State: resp.State}, &rr)
	if rr.Diagnostics.HasError() || !rr.State.Raw.IsNull() {
		t.Errorf("a missing VPC must be removed from state: %v", rr.Diagnostics)
	}
	// Deleting a VPC that still has a VM shows the server's reason.
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	f.cards["web1"] = &fakeCard{VPC: "backend"}
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: resp.State}, &dr)
	if !dr.Diagnostics.HasError() || !strings.Contains(dr.Diagnostics.Errors()[0].Detail(), "still has VMs") {
		t.Errorf("delete = %v", dr.Diagnostics)
	}
}

func TestSecurityGroupAndRule(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}

	gr := &SecurityGroupResource{client: c}
	gsch := schemaOf(gr)
	gplan := SecurityGroupResourceModel{ID: types.StringUnknown(), VPC: types.StringValue("backend"), Name: types.StringValue("web"), Description: types.StringValue("web tier")}
	gresp := resource.CreateResponse{State: stateFor[SecurityGroupResourceModel](t, gsch, nil)}
	gr.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, gsch, &gplan))}, &gresp)
	if gresp.Diagnostics.HasError() {
		t.Fatal(gresp.Diagnostics)
	}
	var g SecurityGroupResourceModel
	gresp.State.Get(ctx, &g)
	if g.ID.ValueString() != "backend/web" {
		t.Errorf("id = %v", g.ID)
	}

	rr := &SecurityGroupRuleResource{client: c}
	rsch := schemaOf(rr)
	plan := SecurityGroupRuleResourceModel{ID: types.StringUnknown(), VPC: types.StringValue("backend"), SecurityGroup: types.StringValue("web"),
		Direction: types.StringValue("ingress"), Protocol: types.StringValue("tcp"), PortFrom: types.Int64Value(443), PortTo: types.Int64Unknown(),
		CIDR: types.StringValue("0.0.0.0/0"), SourceGroup: types.StringNull(), Description: types.StringValue(""), RuleID: types.Int64Unknown()}
	resp := resource.CreateResponse{State: stateFor[SecurityGroupRuleResourceModel](t, rsch, nil)}
	rr.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, rsch, &plan))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var rule SecurityGroupRuleResourceModel
	resp.State.Get(ctx, &rule)
	if rule.PortTo.ValueInt64() != 443 || rule.ID.ValueString() != "backend/web/1" || rule.SourceGroup.IsUnknown() || !rule.SourceGroup.IsNull() {
		t.Errorf("rule = %+v", rule)
	}
	// Read finds the rule; once it is gone from the server the resource leaves the state.
	rd := resource.ReadResponse{State: resp.State}
	rr.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if rd.Diagnostics.HasError() || rd.State.Raw.IsNull() {
		t.Fatalf("read: %v", rd.Diagnostics)
	}
	f.groups["backend"][0].Rules = nil
	rd = resource.ReadResponse{State: resp.State}
	rr.Read(ctx, resource.ReadRequest{State: resp.State}, &rd)
	if !rd.State.Raw.IsNull() {
		t.Error("a rule removed on the server must leave the state")
	}
	// A rule's peer can be a group instead of a range.
	plan.CIDR, plan.SourceGroup, plan.Protocol, plan.PortFrom = types.StringNull(), types.StringValue("web"), types.StringValue("all"), types.Int64Null()
	plan.PortTo = types.Int64Null()
	resp = resource.CreateResponse{State: stateFor[SecurityGroupRuleResourceModel](t, rsch, nil)}
	rr.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, rsch, &plan))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	resp.State.Get(ctx, &rule)
	if rule.SourceGroup.ValueString() != "web" || !rule.CIDR.IsNull() || rule.PortFrom.ValueInt64() != 0 {
		t.Errorf("group rule = %+v", rule)
	}

	// Remove the rule, then the group.
	dr := resource.DeleteResponse{}
	rr.Delete(ctx, resource.DeleteRequest{State: resp.State}, &dr)
	gr.Delete(ctx, resource.DeleteRequest{State: gresp.State}, &dr)
	if dr.Diagnostics.HasError() || len(f.groups["backend"]) != 0 {
		t.Errorf("delete: %v, groups left %d", dr.Diagnostics, len(f.groups["backend"]))
	}
}

func TestSecurityGroupRuleValidation(t *testing.T) {
	ctx := context.Background()
	r := &SecurityGroupRuleResource{}
	sch := schemaOf(r)
	base := func() SecurityGroupRuleResourceModel {
		return SecurityGroupRuleResourceModel{ID: types.StringNull(), VPC: types.StringValue("b"), SecurityGroup: types.StringValue("w"),
			Direction: types.StringValue("ingress"), Protocol: types.StringValue("tcp"), PortFrom: types.Int64Null(), PortTo: types.Int64Null(),
			CIDR: types.StringValue("0.0.0.0/0"), SourceGroup: types.StringNull(), Description: types.StringNull(), RuleID: types.Int64Null()}
	}
	cases := map[string]func(*SecurityGroupRuleResourceModel){
		"ok":              func(m *SecurityGroupRuleResourceModel) {},
		"both peers":      func(m *SecurityGroupRuleResourceModel) { m.SourceGroup = types.StringValue("x") },
		"no peer":         func(m *SecurityGroupRuleResourceModel) { m.CIDR = types.StringNull() },
		"bad direction":   func(m *SecurityGroupRuleResourceModel) { m.Direction = types.StringValue("in") },
		"bad protocol":    func(m *SecurityGroupRuleResourceModel) { m.Protocol = types.StringValue("sctp") },
		"ports with icmp": func(m *SecurityGroupRuleResourceModel) { m.Protocol = types.StringValue("icmp"); m.PortFrom = types.Int64Value(8) },
		"unknown peer ok": func(m *SecurityGroupRuleResourceModel) { m.CIDR = types.StringUnknown() },
	}
	want := map[string]bool{"ok": false, "both peers": true, "no peer": true, "bad direction": true, "bad protocol": true, "ports with icmp": true, "unknown peer ok": false}
	for name, mod := range cases {
		m := base()
		mod(&m)
		st := stateFor(t, sch, &m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: st.Schema, Raw: st.Raw}}, &resp)
		if resp.Diagnostics.HasError() != want[name] {
			t.Errorf("%s: error=%v want %v (%v)", name, resp.Diagnostics.HasError(), want[name], resp.Diagnostics)
		}
	}
}

func TestFloatingIPLifecycle(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	r := &FloatingIPResource{client: c}
	sch := schemaOf(r)

	// Allocate and attach in one step.
	plan := FloatingIPResourceModel{ID: types.StringUnknown(), Address: types.StringUnknown(), VPC: types.StringValue("backend"), VM: types.StringValue("web1")}
	resp := resource.CreateResponse{State: stateFor[FloatingIPResourceModel](t, sch, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, sch, &plan))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var st FloatingIPResourceModel
	resp.State.Get(ctx, &st)
	addr := st.Address.ValueString()
	if addr == "" || f.floats[addr][1] != "web1" {
		t.Fatalf("state %+v, server %v", st, f.floats)
	}

	// Point it at another VM: detach, then attach.
	next := FloatingIPResourceModel{ID: st.ID, Address: st.Address, VPC: types.StringValue("backend"), VM: types.StringValue("web2")}
	ur := resource.UpdateResponse{State: resp.State}
	r.Update(ctx, resource.UpdateRequest{Plan: planOf(stateFor(t, sch, &next)), State: resp.State}, &ur)
	if ur.Diagnostics.HasError() || f.floats[addr][1] != "web2" {
		t.Fatalf("update: %v, server %v", ur.Diagnostics, f.floats)
	}

	// Drift: someone detached it in the UI. Read shows it unattached.
	f.floats[addr] = [2]string{}
	rd := resource.ReadResponse{State: ur.State}
	r.Read(ctx, resource.ReadRequest{State: ur.State}, &rd)
	rd.State.Get(ctx, &st)
	if !st.VM.IsNull() || !st.VPC.IsNull() {
		t.Errorf("a detached address must read back with no VM: %+v", st)
	}

	// Delete detaches first when needed, then releases.
	f.floats[addr] = [2]string{"backend", "web2"}
	st.VPC, st.VM = types.StringValue("backend"), types.StringValue("web2")
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: stateFor(t, sch, &st)}, &dr)
	if dr.Diagnostics.HasError() || len(f.floats) != 0 {
		t.Errorf("delete: %v, left %v", dr.Diagnostics, f.floats)
	}
}

func TestFloatingIPAttachFailureReleases(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	r := &FloatingIPResource{client: c}
	sch := schemaOf(r)
	plan := FloatingIPResourceModel{ID: types.StringUnknown(), Address: types.StringUnknown(), VPC: types.StringValue("backend"), VM: types.StringValue("ghost")}
	resp := resource.CreateResponse{State: stateFor[FloatingIPResourceModel](t, sch, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, sch, &plan))}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "ghost") {
		t.Fatalf("expected the attach to fail with the server's reason: %v", resp.Diagnostics)
	}
	if len(f.floats) != 0 {
		t.Errorf("the address must be released when the attach fails, still allocated: %v", f.floats)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("nothing may be saved to state when create failed")
	}
}

func TestFloatingIPNeedsVPCAndVM(t *testing.T) {
	ctx := context.Background()
	r := &FloatingIPResource{}
	sch := schemaOf(r)
	for name, m := range map[string]FloatingIPResourceModel{
		"both":    {ID: types.StringNull(), Address: types.StringNull(), VPC: types.StringValue("b"), VM: types.StringValue("v")},
		"neither": {ID: types.StringNull(), Address: types.StringNull(), VPC: types.StringNull(), VM: types.StringNull()},
		"only vm": {ID: types.StringNull(), Address: types.StringNull(), VPC: types.StringNull(), VM: types.StringValue("v")},
	} {
		st := stateFor(t, sch, &m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: st.Schema, Raw: st.Raw}}, &resp)
		if resp.Diagnostics.HasError() != (name == "only vm") {
			t.Errorf("%s: %v", name, resp.Diagnostics)
		}
	}
}

func groupSet(names ...string) types.Set {
	v := make([]attr.Value, 0, len(names))
	for _, n := range names {
		v = append(v, types.StringValue(n))
	}
	return types.SetValueMust(types.StringType, v)
}

func TestVMCreateInVPC(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	r := &VMResource{client: c}

	plan := baseModel(types.StringUnknown())
	plan.Name = types.StringValue("web1")
	plan.VPC = types.StringValue("backend")
	plan.SecurityGroups = groupSet("web", "ssh")
	plan.VPCIP = types.StringUnknown()
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(ctx, resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if f.launch["vpc"] != "backend" {
		t.Errorf("the launch carried %v", f.launch)
	}
	if g, _ := f.launch["security_groups"].([]any); len(g) != 2 || g[0] != "ssh" || g[1] != "web" {
		t.Errorf("security_groups sent = %v", f.launch["security_groups"])
	}
	var m VMResourceModel
	resp.State.Get(ctx, &m)
	if m.VPC.ValueString() != "backend" || m.VPCIP.ValueString() != "10.20.0.5" || len(m.SecurityGroups.Elements()) != 2 {
		t.Errorf("state: vpc=%v vpc_ip=%v groups=%v", m.VPC, m.VPCIP, m.SecurityGroups)
	}

	// Update the groups in place.
	next := m
	next.SecurityGroups = groupSet("default")
	ur := resource.UpdateResponse{State: resp.State}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, next), State: resp.State}, &ur)
	if ur.Diagnostics.HasError() {
		t.Fatal(ur.Diagnostics)
	}
	if c := f.cards["web1"]; len(c.Groups) != 1 || c.Groups[0] != "default" {
		t.Errorf("server groups = %v", c.Groups)
	}
	ur.State.Get(ctx, &m)
	if len(m.SecurityGroups.Elements()) != 1 {
		t.Errorf("state groups = %v", m.SecurityGroups)
	}

	// Read shows drift: groups changed on the server.
	f.cards["web1"].Groups = []string{"default", "extra"}
	rd := resource.ReadResponse{State: ur.State}
	r.Read(ctx, resource.ReadRequest{State: ur.State}, &rd)
	rd.State.Get(ctx, &m)
	if len(m.SecurityGroups.Elements()) != 2 {
		t.Errorf("read must pick up the server's groups, got %v", m.SecurityGroups)
	}

	// A VM taken out of the VPC reads back with no vpc, so the next plan replaces it.
	delete(f.cards, "web1")
	rd = resource.ReadResponse{State: rd.State}
	r.Read(ctx, resource.ReadRequest{State: rd.State}, &rd)
	rd.State.Get(ctx, &m)
	if !m.VPC.IsNull() || !m.VPCIP.IsNull() || !m.SecurityGroups.IsNull() {
		t.Errorf("state after leaving the VPC: %+v", m)
	}
}

func TestVMWithoutVPCHasNullVPCFields(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	r := &VMResource{client: c}
	plan := baseModel(types.StringUnknown())
	plan.SecurityGroups = types.SetUnknown(types.StringType)
	plan.VPCIP = types.StringUnknown()
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(ctx, resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if _, has := f.launch["vpc"]; has {
		t.Errorf("a VM with no vpc must not send one: %v", f.launch)
	}
	var m VMResourceModel
	resp.State.Get(ctx, &m)
	if !m.VPC.IsNull() || !m.VPCIP.IsNull() || !m.SecurityGroups.IsNull() || m.SecurityGroups.IsUnknown() {
		t.Errorf("state = %+v", m)
	}
	for _, c := range f.log() {
		if strings.Contains(c, "/vpcs") {
			t.Errorf("a VM with no vpc must not call the VPC API: %v", f.log())
		}
	}
}

func TestVMConfigValidation(t *testing.T) {
	ctx := context.Background()
	r := &VMResource{}
	cases := map[string]func(*VMResourceModel) bool{
		"groups without vpc": func(m *VMResourceModel) bool { m.SecurityGroups = groupSet("a"); return true },
		"vpc with bridged":   func(m *VMResourceModel) bool { m.VPC = types.StringValue("b"); m.Bridged = types.BoolValue(true); return true },
		"six groups": func(m *VMResourceModel) bool {
			m.VPC = types.StringValue("b")
			m.SecurityGroups = groupSet("a", "b", "c", "d", "e", "f")
			return true
		},
		"vpc and groups": func(m *VMResourceModel) bool { m.VPC = types.StringValue("b"); m.SecurityGroups = groupSet("a"); return false },
		"nothing":        func(m *VMResourceModel) bool { return false },
		"vpc unknown":    func(m *VMResourceModel) bool { m.VPC = types.StringUnknown(); m.SecurityGroups = groupSet("a"); return false },
	}
	for name, mod := range cases {
		m := baseModel(types.StringNull())
		m.Bridged = types.BoolNull()
		wantErr := mod(&m)
		st := asState(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: st.Schema, Raw: st.Raw}}, &resp)
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("%s: error=%v want %v (%v)", name, resp.Diagnostics.HasError(), wantErr, resp.Diagnostics)
		}
	}
}

func TestVMImportFindsVPC(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	f.cards["web1"] = &fakeCard{VPC: "backend", IP: "10.20.0.9", Groups: []string{"default"}}
	r := &VMResource{client: c}
	resp := resource.ImportStateResponse{State: emptyState(t)}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "web1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m VMResourceModel
	resp.State.Get(ctx, &m)
	if m.VPC.ValueString() != "backend" || m.VPCIP.ValueString() != "10.20.0.9" {
		t.Errorf("imported vpc=%v ip=%v", m.VPC, m.VPCIP)
	}
}

// The state can be older than the server. A destroy must still finish: it detaches whatever is attached, and an
// address the server no longer knows is already gone.
func TestFloatingIPDeleteSurvivesStaleState(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	r := &FloatingIPResource{client: c}
	sch := schemaOf(r)

	// The state says unattached, the server says attached (a VM was replaced and the address followed it).
	f.floats["192.0.2.50"] = [2]string{"backend", "web1"}
	st := FloatingIPResourceModel{ID: types.StringValue("192.0.2.50"), Address: types.StringValue("192.0.2.50"), VPC: types.StringNull(), VM: types.StringNull()}
	dr := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: stateFor(t, sch, &st)}, &dr)
	if dr.Diagnostics.HasError() || len(f.floats) != 0 {
		t.Errorf("stale unattached state: %v, left %v", dr.Diagnostics, f.floats)
	}

	// The state says attached, the server has nothing.
	st = FloatingIPResourceModel{ID: types.StringValue("192.0.2.51"), Address: types.StringValue("192.0.2.51"), VPC: types.StringValue("backend"), VM: types.StringValue("web1")}
	dr = resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: stateFor(t, sch, &st)}, &dr)
	if dr.Diagnostics.HasError() {
		t.Errorf("an address the server no longer has must not fail the destroy: %v", dr.Diagnostics)
	}
}

// An address left allocated by a run that stopped half way is taken over when the config names it.
func TestFloatingIPCreateTakesOverAnUnattachedLeftover(t *testing.T) {
	ctx := context.Background()
	c, f := newVPCServer(t)
	f.vpcs["backend"] = map[string]any{"cidr": "10.20.0.0/16", "mtu": 1450}
	f.floats["192.0.2.60"] = [2]string{}
	r := &FloatingIPResource{client: c}
	sch := schemaOf(r)
	plan := FloatingIPResourceModel{ID: types.StringUnknown(), Address: types.StringValue("192.0.2.60"), VPC: types.StringValue("backend"), VM: types.StringValue("web1")}
	resp := resource.CreateResponse{State: stateFor[FloatingIPResourceModel](t, sch, nil)}
	r.Create(ctx, resource.CreateRequest{Plan: planOf(stateFor(t, sch, &plan))}, &resp)
	if resp.Diagnostics.HasError() || f.floats["192.0.2.60"][1] != "web1" {
		t.Fatalf("create: %v, server %v", resp.Diagnostics, f.floats)
	}
}
