package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakeServer stands in for one virtuOSo VM. It keeps the notes the way the real server does and
// records every request, so tests can see exactly what the provider sent.
type fakeServer struct {
	mu        sync.Mutex
	notes     string
	noteCalls []string
	notesCode int // when not zero, PUT /notes fails with this status
	calls     []string
}

func newFakeServer(t *testing.T, notes string) (*VirtuosoClient, *fakeServer) {
	t.Helper()
	f := &fakeServer{notes: notes}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/vms":
			w.WriteHeader(202)
			w.Write([]byte(`{"name":"web1","password":"pw","status":"creating"}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/vms/web1":
			json.NewEncoder(w).Encode(map[string]any{
				"name": "web1", "state": "running", "ip": "192.0.2.10", "vcpus": 2, "memory_mb": 2048,
				"autostart": "disable", "network": "default", "notes": f.notes, "os_id": "ubuntu-24.04",
				"disk_cap_gb": 20.0,
			})
		case r.Method == "PUT" && r.URL.Path == "/api/v1/vms/web1/notes":
			var req struct{ Notes *string }
			json.Unmarshal(body, &req)
			f.noteCalls = append(f.noteCalls, string(body))
			if f.notesCode != 0 {
				w.WriteHeader(f.notesCode)
				w.Write([]byte(`{"error":"notes are too large (limit 64 KB)","status":413}`))
				return
			}
			if req.Notes == nil {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"notes is required","status":400}`))
				return
			}
			f.notes = *req.Notes
			w.Write([]byte(`{"vm":"web1"}`))
		default:
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)
	return NewVirtuosoClient(ts.URL, "test-key", false), f
}

func (f *fakeServer) storedNotes() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notes
}

func (f *fakeServer) notePuts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.noteCalls)
}

func TestClientNotesCalls(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`{"name":"web1","state":"running","notes":"hello\nworld"}`))
			return
		}
		w.Write([]byte(`{"vm":"web1","notes":"x"}`))
	})

	vm, err := c.GetVM("web1")
	if err != nil || vm.Notes != "hello\nworld" {
		t.Fatalf("GetVM notes = %q (%v)", vm.Notes, err)
	}

	text := "quote \" slash \\ newline \n tab \t <b>html</b> 中文"
	if err := c.SetNotes("web1", text); err != nil {
		t.Fatal(err)
	}
	if rec.method != "PUT" || rec.path != "/api/v1/vms/web1/notes" || rec.auth != "Bearer test-key" {
		t.Errorf("request = %+v", *rec)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if body["notes"] != text || len(body) != 1 {
		t.Errorf("body = %s", rec.body)
	}

	// An empty string is sent as an empty string, so Terraform can clear notes.
	if err := c.SetNotes("web1", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.body, `"notes":""`) {
		t.Errorf("clear body = %s", rec.body)
	}
}

func TestClientNotesErrorsAreReported(t *testing.T) {
	for _, code := range []int{400, 404, 413, 500} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"error":"notes are too large (limit 64 KB)","status":0}`))
		})
		err := c.SetNotes("web1", "x")
		if err == nil {
			t.Errorf("HTTP %d must be an error", code)
		} else if !strings.Contains(err.Error(), "64 KB") {
			t.Errorf("HTTP %d: the server's reason is lost: %v", code, err)
		}
	}
}

func TestVMResourceNotesSchema(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("invalid schema: %v", diags)
	}
	attr, ok := resp.Schema.Attributes["notes"]
	if !ok {
		t.Fatal("virtuoso_vm has no notes attribute")
	}
	if !attr.IsOptional() || !attr.IsComputed() || attr.IsRequired() {
		t.Error("notes must be optional and computed, so leaving it out keeps the server's notes")
	}
	if attr.IsSensitive() {
		t.Error("notes are not secrets, and hiding them would hide the plan diff")
	}
	// Computed values carried over from state stop an update from showing "known after apply" for
	// text that Terraform is not changing.
	mods := attr.(interface{ StringPlanModifiers() []planmodifier.String }).StringPlanModifiers()
	if len(mods) != 1 {
		t.Fatalf("notes needs exactly UseStateForUnknown, has %d plan modifiers", len(mods))
	}
	// Notes are changed in place, never by replacing the VM.
	for _, m := range mods {
		if strings.Contains(m.Description(context.Background()), "replace") {
			t.Error("a change to notes must not replace the VM")
		}
	}
}

func TestVMDataSourceNotesSchema(t *testing.T) {
	var resp datasource.SchemaResponse
	NewVMDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	attr, ok := resp.Schema.Attributes["notes"]
	if !ok || !attr.IsComputed() || attr.IsRequired() || attr.IsOptional() {
		t.Fatal("the virtuoso_vm data source must expose notes as a computed attribute")
	}
	// The list data source stays light: no notes for every VM.
	var list datasource.SchemaResponse
	NewVMsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &list)
	if _, ok := list.Schema.Attributes["notes"]; ok {
		t.Error("virtuoso_vms should not carry notes for every VM")
	}
}

// --- driving the resource itself ---

func vmSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var resp resource.SchemaResponse
	NewVMResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp
}

func baseModel(notes types.String) VMResourceModel {
	return VMResourceModel{
		Name: types.StringValue("web1"), Size: types.StringValue("small"), OS: types.StringValue("ubuntu-24.04"),
		SSHKey: types.StringNull(), DiskGB: types.Int64Value(20), Password: types.StringValue("pw"),
		VNC: types.BoolNull(), Desktop: types.BoolNull(), UserScript: types.StringNull(),
		PostTailscaleScript: types.StringNull(), ISO: types.StringNull(), Bridged: types.BoolValue(false),
		Autostart: types.BoolValue(false), Protected: types.BoolValue(false), Started: types.BoolValue(true),
		Notes: notes, Tags: types.MapNull(types.StringType),
		IP:    types.StringValue("192.0.2.10"), State: types.StringValue("running"), VCPUs: types.Int64Value(2),
		MemoryMB: types.Int64Value(2048), DiskCapGB: types.Float64Value(20), DiskUsedGB: types.Float64Value(1),
		HasVNC: types.BoolValue(false), CloudInit: types.StringValue("done"), Network: types.StringValue("default"),
		OSID: types.StringValue("ubuntu-24.04"),
	}
}

func asState(t *testing.T, m VMResourceModel) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	sch := vmSchema(t).Schema
	st := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	if diags := st.Set(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}
	return st
}

func asPlan(t *testing.T, m VMResourceModel) tfsdk.Plan {
	t.Helper()
	st := asState(t, m)
	return tfsdk.Plan{Schema: st.Schema, Raw: st.Raw}
}

func emptyState(t *testing.T) tfsdk.State {
	ctx := context.Background()
	sch := vmSchema(t).Schema
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
}

func notesOf(t *testing.T, st tfsdk.State) types.String {
	t.Helper()
	var m VMResourceModel
	if diags := st.Get(context.Background(), &m); diags.HasError() {
		t.Fatal(diags)
	}
	return m.Notes
}

func TestVMCreateWithNotes(t *testing.T) {
	c, f := newFakeServer(t, "")
	r := &VMResource{client: c}
	text := "# web1\n\nowner: ops"
	plan := baseModel(types.StringValue(text))
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := f.storedNotes(); got != text {
		t.Fatalf("the server holds %q, want %q", got, text)
	}
	if got := notesOf(t, resp.State); got.ValueString() != text {
		t.Fatalf("state notes = %q", got.ValueString())
	}
}

// With notes left out the plan value is unknown. Create must not send anything, and must
// store what the server reports so there is no "inconsistent result after apply".
func TestVMCreateWithoutNotes(t *testing.T) {
	for name, planned := range map[string]types.String{
		"unknown (omitted)": types.StringUnknown(), "null": types.StringNull(), "empty string": types.StringValue(""),
	} {
		c, f := newFakeServer(t, "")
		r := &VMResource{client: c}
		resp := resource.CreateResponse{State: emptyState(t)}
		r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, baseModel(planned))}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if f.notePuts() != 0 {
			t.Errorf("%s: no notes were configured but %d note writes were sent", name, f.notePuts())
		}
		if got := notesOf(t, resp.State); got.IsNull() || got.IsUnknown() || got.ValueString() != "" {
			t.Errorf("%s: state notes should be a known empty string, got %v", name, got)
		}
	}
}

func TestVMCreateReportsNotesFailure(t *testing.T) {
	c, f := newFakeServer(t, "")
	f.notesCode = 413
	r := &VMResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, baseModel(types.StringValue("x")))}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused notes write must fail the apply")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "64 KB") {
		t.Errorf("the reason is lost: %v", resp.Diagnostics)
	}
}

// The point of optional plus computed: a plan that does not mention notes must never write them,
// so notes edited in the web UI are not wiped by the next apply.
func TestVMUpdateKeepsNotesWhenNotConfigured(t *testing.T) {
	for name, planned := range map[string]types.String{
		"carried from state (UseStateForUnknown)": types.StringValue("written in the web UI"),
		"unknown": types.StringUnknown(),
		"null":    types.StringNull(),
	} {
		c, f := newFakeServer(t, "written in the web UI")
		r := &VMResource{client: c}
		state := baseModel(types.StringValue("written in the web UI"))
		plan := baseModel(planned)
		plan.Autostart = types.BoolValue(false)
		resp := resource.UpdateResponse{State: asState(t, state)}
		r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, plan), State: asState(t, state)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		if f.notePuts() != 0 {
			t.Errorf("%s: an update that does not change notes wrote them", name)
		}
		if got := f.storedNotes(); got != "written in the web UI" {
			t.Errorf("%s: server notes became %q", name, got)
		}
		if got := notesOf(t, resp.State); got.IsUnknown() {
			t.Errorf("%s: state notes must be known after apply", name)
		}
	}
}

func TestVMUpdateChangesAndClearsNotes(t *testing.T) {
	c, f := newFakeServer(t, "old")
	r := &VMResource{client: c}
	ctx := context.Background()

	state := baseModel(types.StringValue("old"))
	resp := resource.UpdateResponse{State: asState(t, state)}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, baseModel(types.StringValue("new text"))), State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if f.storedNotes() != "new text" || f.notePuts() != 1 {
		t.Fatalf("server notes %q after %d writes", f.storedNotes(), f.notePuts())
	}
	if got := notesOf(t, resp.State); got.ValueString() != "new text" {
		t.Fatalf("state notes = %q", got.ValueString())
	}

	// Setting the empty string clears them, and that is a real write.
	state = baseModel(types.StringValue("new text"))
	resp = resource.UpdateResponse{State: asState(t, state)}
	r.Update(ctx, resource.UpdateRequest{Plan: asPlan(t, baseModel(types.StringValue(""))), State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if f.storedNotes() != "" || f.notePuts() != 2 {
		t.Fatalf("server notes %q after %d writes", f.storedNotes(), f.notePuts())
	}
	if got := notesOf(t, resp.State); got.IsNull() || got.ValueString() != "" {
		t.Fatalf("cleared notes should read back as an empty string, got %v", got)
	}
}

func TestVMUpdateReportsNotesFailure(t *testing.T) {
	c, f := newFakeServer(t, "old")
	f.notesCode = 413
	r := &VMResource{client: c}
	state := baseModel(types.StringValue("old"))
	resp := resource.UpdateResponse{State: asState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: asPlan(t, baseModel(types.StringValue("too big"))), State: asState(t, state)}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused notes write must fail the apply")
	}
}

// Notes edited outside Terraform show up as drift on the next refresh.
func TestVMReadPicksUpNotesChangedInTheUI(t *testing.T) {
	c, _ := newFakeServer(t, "edited in the web UI")
	r := &VMResource{client: c}
	state := baseModel(types.StringValue("what Terraform last saw"))
	resp := resource.ReadResponse{State: asState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := notesOf(t, resp.State); got.ValueString() != "edited in the web UI" {
		t.Fatalf("state notes = %q", got.ValueString())
	}
}

// Importing a VM that already has notes must show them, not an empty value.
func TestVMReadAfterImportShowsNotes(t *testing.T) {
	c, _ := newFakeServer(t, "already there")
	r := &VMResource{client: c}
	state := baseModel(types.StringNull())
	resp := resource.ReadResponse{State: asState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := notesOf(t, resp.State); got.ValueString() != "already there" {
		t.Fatalf("state notes = %v", got)
	}
}

// A server that predates this feature returns no notes field. That must read as empty, not fail.
func TestVMReadFromOlderServerWithoutNotes(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"web1","state":"running","ip":"192.0.2.10","network":"default"}`))
	})
	r := &VMResource{client: c}
	state := baseModel(types.StringValue(""))
	resp := resource.ReadResponse{State: asState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: asState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if got := notesOf(t, resp.State); got.IsNull() || got.IsUnknown() || got.ValueString() != "" {
		t.Fatalf("expected a known empty string, got %v", got)
	}
}
