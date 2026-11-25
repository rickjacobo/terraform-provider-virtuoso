package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// tagServer keeps one VM's tags the way virtuOSo does and records every request.
type tagServer struct {
	mu     sync.Mutex
	tags   map[string]string
	calls  []string
	launch map[string]any
	code   int // when not zero, tag writes fail with this status
}

func newTagServer(t *testing.T, tags map[string]string) (*VirtuosoClient, *tagServer) {
	t.Helper()
	f := &tagServer{tags: tags}
	if f.tags == nil {
		f.tags = map[string]string{}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/vms":
			json.Unmarshal(body, &f.launch)
			if t, ok := f.launch["tags"].(map[string]any); ok {
				for k, v := range t {
					f.tags[k] = v.(string)
				}
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"name":"web1","password":"pw","status":"creating"}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/vms/web1":
			out := map[string]any{"name": "web1", "state": "running", "ip": "192.0.2.10", "vcpus": 2, "memory_mb": 2048,
				"autostart": "disable", "network": "default", "os_id": "ubuntu-24.04", "disk_cap_gb": 20.0}
			if len(f.tags) > 0 { // the server leaves the field out when there are none
				out["tags"] = f.tags
			}
			json.NewEncoder(w).Encode(out)
		case r.Method == "POST" && r.URL.Path == "/api/v1/vms/web1/tags":
			f.calls = append(f.calls, "POST "+string(body))
			if f.code != 0 {
				w.WriteHeader(f.code)
				w.Write([]byte(`{"error":"a VM can have at most 20 tags","status":400}`))
				return
			}
			var req struct{ Tags map[string]string }
			json.Unmarshal(body, &req)
			for k, v := range req.Tags {
				f.tags[k] = v
			}
			json.NewEncoder(w).Encode(map[string]any{"vm": "web1", "tags": f.tags})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/vms/web1/tags/"):
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/vms/web1/tags/")
			f.calls = append(f.calls, "DELETE "+key)
			if _, ok := f.tags[key]; !ok {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":"this VM has no tag","status":404}`))
				return
			}
			delete(f.tags, key)
			json.NewEncoder(w).Encode(map[string]any{"vm": "web1", "tags": f.tags})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(ts.Close)
	return NewVirtuosoClient(ts.URL, "test-key", true), f
}

func (f *tagServer) stored() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.tags {
		out[k] = v
	}
	return out
}

func (f *tagServer) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func tagMap(kv ...string) types.Map {
	m := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, m)
}

func TestClientTagCalls(t *testing.T) {
	c, f := newTagServer(t, map[string]string{"old": "1", "keep": "x"})
	if err := c.ReplaceTags("web1", map[string]string{"old": "1", "keep": "x"}, map[string]string{"keep": "y", "new": ""}); err != nil {
		t.Fatal(err)
	}
	got := f.stored()
	if len(got) != 2 || got["keep"] != "y" || got["new"] != "" {
		t.Errorf("tags = %v", got)
	}
	// Removals go first, so a full VM can swap tags without hitting the limit. Only what changed is sent.
	calls := f.callList()
	if len(calls) != 2 || calls[0] != "DELETE old" || !strings.HasPrefix(calls[1], "POST ") || strings.Contains(calls[1], `"x"`) {
		t.Errorf("calls = %v", calls)
	}
	// Nothing to change sends nothing.
	n := len(calls)
	if err := c.ReplaceTags("web1", got, got); err != nil || len(f.callList()) != n {
		t.Errorf("an unchanged set must send nothing: %v %v", err, f.callList())
	}
	// A tag that is already gone is not an error; the server's reason is kept for a real refusal.
	if err := c.RemoveTag("web1", "ghost"); err != nil {
		t.Errorf("a missing tag: %v", err)
	}
	f.code = 400
	if err := c.SetTags("web1", map[string]string{"a": "b"}); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Errorf("the server's reason is lost: %v", err)
	}
}

func TestVMTagsSchema(t *testing.T) {
	ctx := context.Background()
	a, ok := vmSchema(t).Schema.Attributes["tags"]
	if !ok || !a.IsOptional() || !a.IsComputed() || a.IsRequired() {
		t.Fatal("tags must be optional and computed, so leaving it out keeps the server's tags")
	}
	if diags := vmSchema(t).Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatal(diags)
	}
	var ds datasource.SchemaResponse
	NewVMDataSource().Schema(ctx, datasource.SchemaRequest{}, &ds)
	if x, ok := ds.Schema.Attributes["tags"]; !ok || !x.IsComputed() || x.IsOptional() {
		t.Error("the virtuoso_vm data source must expose tags as computed")
	}
	var list datasource.SchemaResponse
	NewVMsDataSource().Schema(ctx, datasource.SchemaRequest{}, &list)
	if !strings.Contains(string(mustJSON(list.Schema.Attributes["vms"])), "tags") {
		t.Error("virtuoso_vms must carry each VM's tags")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func stateTags(t *testing.T, st tfsdk.State) map[string]string {
	t.Helper()
	var m VMResourceModel
	if diags := st.Get(context.Background(), &m); diags.HasError() {
		t.Fatal(diags)
	}
	if m.Tags.IsNull() || m.Tags.IsUnknown() {
		t.Fatalf("state tags must be known, got %v", m.Tags)
	}
	out := map[string]string{}
	m.Tags.ElementsAs(context.Background(), &out, false)
	return out
}

func TestVMCreateSendsTagsAtLaunch(t *testing.T) {
	c, f := newTagServer(t, nil)
	r := &VMResource{client: c}
	plan := baseModel(types.StringUnknown())
	plan.Tags = tagMap("env", "test", "keep", "")
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if tags, _ := f.launch["tags"].(map[string]any); tags["env"] != "test" || len(tags) != 2 {
		t.Errorf("the launch carried %v", f.launch)
	}
	if got := stateTags(t, resp.State); got["env"] != "test" || len(got) != 2 {
		t.Errorf("state tags = %v", got)
	}
}

func TestVMCreateWithoutTags(t *testing.T) {
	for name, planned := range map[string]types.Map{"unknown (omitted)": types.MapUnknown(types.StringType), "null": types.MapNull(types.StringType), "empty": tagMap()} {
		c, f := newTagServer(t, nil)
		r := &VMResource{client: c}
		plan := baseModel(types.StringUnknown())
		plan.Tags = planned
		resp := resource.CreateResponse{State: emptyState(t)}
		r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if _, has := f.launch["tags"]; has || len(f.callList()) != 0 {
			t.Errorf("%s: tags were sent: %v %v", name, f.launch, f.callList())
		}
		if got := stateTags(t, resp.State); len(got) != 0 {
			t.Errorf("%s: state tags = %v", name, got)
		}
	}
}

func TestVMUpdateReplacesTagsExactly(t *testing.T) {
	// "added-in-ui" was put on the VM outside Terraform. Setting tags here removes it.
	c, f := newTagServer(t, map[string]string{"env": "test", "old": "1", "added-in-ui": "x"})
	r := &VMResource{client: c}
	state := baseModel(types.StringUnknown())
	state.Tags = tagMap("env", "test", "old", "1")
	plan := baseModel(types.StringUnknown())
	plan.Tags = tagMap("env", "prod", "owner", "ops")
	resp := resource.UpdateResponse{State: asState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, plan), State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	got := f.stored()
	keys := []string{}
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "env,owner" || got["env"] != "prod" || got["owner"] != "ops" {
		t.Errorf("the server holds %v", got)
	}
	if st := stateTags(t, resp.State); len(st) != 2 || st["env"] != "prod" {
		t.Errorf("state tags = %v", st)
	}

	// An empty map clears every tag.
	state.Tags = tagMap("env", "prod", "owner", "ops")
	plan.Tags = tagMap()
	resp = resource.UpdateResponse{State: asState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, plan), State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() || len(f.stored()) != 0 {
		t.Errorf("clear: %v %v", resp.Diagnostics, f.stored())
	}
}

// Leaving tags out must never write them, so tags set in the web UI survive the next apply.
func TestVMUpdateKeepsTagsWhenNotConfigured(t *testing.T) {
	for name, planned := range map[string]types.Map{
		"carried from state": tagMap("env", "test"), "unknown": types.MapUnknown(types.StringType), "null": types.MapNull(types.StringType),
	} {
		c, f := newTagServer(t, map[string]string{"env": "test"})
		r := &VMResource{client: c}
		state := baseModel(types.StringUnknown())
		state.Tags = tagMap("env", "test")
		plan := baseModel(types.StringUnknown())
		plan.Tags = planned
		resp := resource.UpdateResponse{State: asState(t, state)}
		r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, plan), State: asState(t, state)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if len(f.callList()) != 0 {
			t.Errorf("%s: an update that does not change tags wrote them: %v", name, f.callList())
		}
	}
}

func TestVMUpdateReportsTagFailure(t *testing.T) {
	c, f := newTagServer(t, nil)
	f.code = 400
	r := &VMResource{client: c}
	state := baseModel(types.StringUnknown())
	state.Tags = tagMap()
	plan := baseModel(types.StringUnknown())
	plan.Tags = tagMap("a", "b")
	resp := resource.UpdateResponse{State: asState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, plan), State: asState(t, state)}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "at most 20") {
		t.Fatalf("a refused tag write must fail the apply with the reason: %v", resp.Diagnostics)
	}
}

// Tags changed in the web UI show up as drift; a VM with none reads as an empty map, never null.
func TestVMReadPicksUpTags(t *testing.T) {
	c, _ := newTagServer(t, map[string]string{"env": "test"})
	r := &VMResource{client: c}
	state := baseModel(types.StringUnknown())
	state.Tags = tagMap()
	resp := resource.ReadResponse{State: asState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := stateTags(t, resp.State); got["env"] != "test" {
		t.Errorf("state tags = %v", got)
	}
	c2, _ := newTagServer(t, nil)
	r = &VMResource{client: c2}
	resp = resource.ReadResponse{State: asState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: asState(t, state)}, &resp)
	if got := stateTags(t, resp.State); len(got) != 0 {
		t.Errorf("a VM with no tags read as %v", got)
	}
}

// The data sources carry what a running VM uses now, and a figure the server left out reads as null, not zero.
func TestVMUsageFiguresAreOptionalReadings(t *testing.T) {
	ctx := context.Background()
	var one, many datasource.SchemaResponse
	NewVMDataSource().Schema(ctx, datasource.SchemaRequest{}, &one)
	NewVMsDataSource().Schema(ctx, datasource.SchemaRequest{}, &many)
	for _, k := range []string{"cpu_percent", "memory_used_mb"} {
		if a, ok := one.Schema.Attributes[k]; !ok || !a.IsComputed() || a.IsOptional() {
			t.Errorf("virtuoso_vm must expose %s as computed", k)
		}
		if !strings.Contains(string(mustJSON(many.Schema.Attributes["vms"])), k) {
			t.Errorf("virtuoso_vms must carry %s", k)
		}
	}
	if !usageFloat(nil).IsNull() || !usageInt(nil).IsNull() {
		t.Error("a missing figure must be null")
	}
	f, i := 12.5, int64(2048)
	if usageFloat(&f).ValueFloat64() != 12.5 || usageInt(&i).ValueInt64() != 2048 {
		t.Error("a figure must be passed on")
	}
	var vm VMResponse
	if err := json.Unmarshal([]byte(`{"name":"a","cpu_percent":0,"memory_used_mb":0}`), &vm); err != nil || vm.CPUPercent == nil || *vm.CPUPercent != 0 {
		t.Errorf("a measured zero is a reading, not a missing figure: %+v %v", vm, err)
	}
}
