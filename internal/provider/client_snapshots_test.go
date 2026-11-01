package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

func TestNormalizeDays(t *testing.T) {
	for in, want := range map[string]string{
		"": "everyday", "Everyday": "everyday", "daily": "everyday",
		"weekdays": "weekdays", "WEEKENDS": "weekends",
		"mon,tue,wed,thu,fri": "weekdays", "Fri, Mon, Thu, Wed, Tue": "weekdays",
		"sat,sun": "weekends", "sun, sat": "weekends",
		"mon,wed,fri": "mon,wed,fri", "Friday, Monday": "mon,fri",
		"mon,tue,wed,thu,fri,sat,sun": "everyday",
	} {
		if got := normalizeDays(in); got != want {
			t.Errorf("normalizeDays(%q) = %q, want %q", in, got, want)
		}
	}
	if !sameDays("Mon, Fri", "mon,fri") || sameDays("mon", "tue") {
		t.Error("sameDays is wrong")
	}
	// Something the server must reject is passed through, not silently fixed.
	if got := normalizeDays("someday"); got != "someday" {
		t.Errorf("unknown day changed to %q", got)
	}
}

func TestScheduleClientCalls(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			w.Write([]byte(`{"vm":"dev","days":"weekdays","start_at":"08:00","stop_at":"19:00","tz":"UTC","enabled":true}`))
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/ghost/"):
			w.WriteHeader(404)
		case r.Method == "GET":
			w.Write([]byte(`{"vm":"dev","days":"weekdays","start_at":"08:00","stop_at":"19:00","tz":"UTC","enabled":true,"last_action":"start"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	})
	got, err := c.PutSchedule(VMSchedule{VM: "dev", Days: "weekdays", StartAt: "08:00", StopAt: "19:00", TZ: "UTC", Enabled: true})
	if err != nil || got.StopAt != "19:00" {
		t.Fatalf("put: %+v %v", got, err)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if rec.method != "PUT" || rec.path != "/api/v1/vms/dev/schedule" || rec.auth != "Bearer test-key" || body["days"] != "weekdays" || body["enabled"] != true {
		t.Errorf("put request = %+v", *rec)
	}
	if s, err := c.GetSchedule("dev"); err != nil || s == nil || s.LastAction != "start" {
		t.Fatalf("get: %+v %v", s, err)
	}
	if s, err := c.GetSchedule("ghost"); err != nil || s != nil {
		t.Fatalf("a missing schedule must read as nil, got %+v %v", s, err)
	}
	if err := c.DeleteSchedule("dev"); err != nil || rec.method != "DELETE" {
		t.Fatalf("delete: %v %+v", err, *rec)
	}
}

func TestScheduleClientSurfacesValidationErrors(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"start time must be HH:MM, 24 hour clock","status":400}`))
	})
	_, err := c.PutSchedule(VMSchedule{VM: "dev", StartAt: "8am"})
	if err == nil || !strings.Contains(err.Error(), "HH:MM") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("the server's reason must reach the user, got %v", err)
	}
}

func TestRestorePolicyClientCalls(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/ghost/") {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"policy":{"vm":"dev","days":"everyday","at":"02:00","tz":"UTC","retention_days":7,"enabled":true},"estimate":{"copies":7,"bytes_total":7000}}`))
	})
	p, est, err := c.PutRestorePolicy(RestorePolicy{VM: "dev", Days: "everyday", At: "02:00", TZ: "UTC", RetentionDays: 7, Enabled: true})
	if err != nil || p.RetentionDays != 7 || est.Copies != 7 || est.Bytes != 7000 {
		t.Fatalf("put: %+v %+v %v", p, est, err)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if rec.path != "/api/v1/vms/dev/restore-policy" || body["retention_days"] != float64(7) || body["at"] != "02:00" {
		t.Errorf("put request = %+v", *rec)
	}
	if p, _, err := c.GetRestorePolicy("ghost"); err != nil || p != nil {
		t.Fatalf("missing policy must read as nil: %+v %v", p, err)
	}
	if err := c.DeleteRestorePolicy("dev"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotClientCalls(t *testing.T) {
	calls := 0
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.WriteHeader(202)
			w.Write([]byte(`{"id":"snap-1","vm":"dev","status":"creating","kind":"full","retention_days":7}`))
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(404)
		case r.Method == "DELETE":
			w.Write([]byte(`{"ok":true}`))
		default: // GET list: creating twice, then ready
			calls++
			status := "creating"
			if calls > 2 {
				status = "ready"
			}
			w.Write([]byte(`{"snapshots":[{"id":"snap-1","vm":"dev","status":"` + status + `","size_bytes":99,"consistency":"clean"}]}`))
		}
	})
	s, err := c.CreateSnapshot("dev", SnapshotCreateRequest{Name: "pre upgrade", RetentionDays: 7})
	if err != nil || s.ID != "snap-1" || s.Status != "creating" {
		t.Fatalf("create: %+v %v", s, err)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if rec.path != "/api/v1/vms/dev/snapshots" || body["name"] != "pre upgrade" || body["retention_days"] != float64(7) {
		t.Errorf("create request = %+v", *rec)
	}
	ready, err := c.WaitForSnapshot("dev", "snap-1", 5*time.Second)
	if err != nil || ready.Status != "ready" || ready.SizeBytes != 99 {
		t.Fatalf("wait: %+v %v", ready, err)
	}
	if err := c.DeleteSnapshot("dev", "snap-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSnapshot("dev", "gone"); err != nil {
		t.Fatalf("deleting a snapshot that is already gone must succeed: %v", err)
	}
}

func TestWaitForSnapshotReportsFailureAndDisappearance(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"snapshots":[{"id":"snap-1","status":"failed","error":"disk full"}]}`))
	})
	if _, err := c.WaitForSnapshot("dev", "snap-1", time.Second); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("a failed snapshot must report why: %v", err)
	}
	c2, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"snapshots":[]}`))
	})
	if _, err := c2.WaitForSnapshot("dev", "snap-1", time.Second); err == nil {
		t.Fatal("a snapshot that vanished while being created is an error")
	}
	c3, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"snapshots":[{"id":"snap-1","status":"creating"}]}`))
	})
	if _, err := c3.WaitForSnapshot("dev", "snap-1", 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected a timeout, got %v", err)
	}
}

func TestSnapshotNotFoundReadsAsNil(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"snapshots":[{"id":"other","status":"ready"}]}`))
	})
	s, err := c.GetSnapshot("dev", "snap-1")
	if err != nil || s != nil {
		t.Fatalf("an expired snapshot must read as nil so Terraform can plan a new one: %+v %v", s, err)
	}
}

func TestResourcesAreRegistered(t *testing.T) {
	got := map[string]bool{}
	for _, f := range (&VirtuosoProvider{}).Resources(context.Background()) {
		var resp resource.MetadataResponse
		f().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "virtuoso"}, &resp)
		got[resp.TypeName] = true
	}
	for _, want := range []string{"virtuoso_vm_schedule", "virtuoso_restore_policy", "virtuoso_snapshot"} {
		if !got[want] {
			t.Errorf("resource %s is not registered", want)
		}
	}
}

func TestNewResourceSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	for _, f := range []func() resource.Resource{NewVMScheduleResource, NewRestorePolicyResource, NewSnapshotResource} {
		var resp resource.SchemaResponse
		f().Schema(ctx, resource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("invalid schema: %v", diags)
		}
	}
}

// Computed values that change with other arguments must not be carried over from the
// old state, or Terraform reports "inconsistent result after apply" on every update.
func TestRestorePolicyEstimateIsNotReusedFromState(t *testing.T) {
	var resp resource.SchemaResponse
	NewRestorePolicyResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, name := range []string{"estimated_copies", "estimated_bytes"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if mods := attr.(interface{ Int64PlanModifiers() []planmodifier.Int64 }).Int64PlanModifiers(); len(mods) != 0 {
			t.Errorf("%s must have no plan modifiers, it changes when retention changes", name)
		}
	}
}

func TestDaysMustBeCanonical(t *testing.T) {
	for in, canon := range map[string]string{
		"Mon, Fri": "mon,fri", "fri,mon": "mon,fri", "mon,tue,wed,thu,fri": "weekdays", "daily": "everyday", "SAT,SUN": "weekends",
	} {
		if got := normalizeDays(in); got != canon || got == in {
			t.Errorf("%q should be rejected in favour of %q (got %q)", in, canon, got)
		}
	}
	for _, ok := range []string{"everyday", "weekdays", "weekends", "mon,wed,fri", "sat"} {
		if normalizeDays(ok) != ok {
			t.Errorf("%q is canonical and must be accepted", ok)
		}
	}
}
