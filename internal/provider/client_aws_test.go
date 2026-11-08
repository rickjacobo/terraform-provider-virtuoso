package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestAWSLaunchSendsTheCostConfirmationAndEverythingAsked(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		w.Write([]byte(`{"name":"web","password":"pw","status":"creating","region":"us-east-1","instance_type":"t3.small","estimated_hourly_usd":0.0208}`))
	})
	out, err := c.LaunchAWSInstance(AWSLaunchRequest{Name: "web", InstanceType: "t3.small", OS: "ubuntu-24.04", SSHKey: "ssh-ed25519 AAAA x", DiskGB: 40,
		Ports: []AWSPort{{Port: 443, Source: "203.0.113.7/32"}}})
	if err != nil || out.Password != "pw" || out.Region != "us-east-1" || out.HourlyUSD != 0.0208 {
		t.Fatalf("launch = %+v, %v", out, err)
	}
	var body map[string]any
	json.Unmarshal([]byte(rec.body), &body)
	if rec.method != "POST" || rec.path != "/api/v1/fleet/aws/instances" || rec.auth != "Bearer test-key" {
		t.Errorf("request = %+v", *rec)
	}
	if body["disk_gb"] != float64(40) {
		t.Errorf("the disk size is sent: %s", rec.body)
	}
	if body["confirm_cost"] != true || body["name"] != "web" || body["instance_type"] != "t3.small" || body["ssh_key"] != "ssh-ed25519 AAAA x" {
		t.Errorf("body = %s", rec.body)
	}
	if ports := body["ports"].([]any); len(ports) != 1 || ports[0].(map[string]any)["port"] != float64(443) || ports[0].(map[string]any)["source"] != "203.0.113.7/32" {
		t.Errorf("ports = %v", body["ports"])
	}
	// Terraform never offers an automatic termination: it would fight the state.
	if strings.Contains(rec.body, "ttl") {
		t.Errorf("the body must not carry a termination time: %s", rec.body)
	}

	// A refusal from the server is shown as it is.
	c2, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"region must be one this account can use: us-east-1","status":400}`))
	})
	if _, err := c2.LaunchAWSInstance(AWSLaunchRequest{Name: "web", Region: "eu-west-1"}); err == nil || !strings.Contains(err.Error(), "region must be one this account can use") {
		t.Errorf("a refusal: %v", err)
	}
}

func TestAWSListAsksForAFreshReadAndReportsProblems(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"configured":true,"loaded":true,"error":"","instances":[{"id":"i-0abc1234def567890","name":"web","region":"us-east-1","type":"t3.small","state":"running","public_ip":"203.0.113.5","private_ip":"192.0.2.5","vcpus":2,"estimated_hourly_usd":0.0208}]}`))
	})
	in, err := c.GetAWSInstance("i-0abc1234def567890")
	if err != nil || in == nil || in.Name != "web" || in.Address() != "203.0.113.5" || in.HourlyUSD == nil || *in.HourlyUSD != 0.0208 {
		t.Fatalf("get = %+v, %v", in, err)
	}
	if rec.path != "/api/v1/fleet/aws" {
		t.Errorf("request = %+v", *rec)
	}
	if gone, err := c.GetAWSInstance("i-0000000000000000f"); err != nil || gone != nil {
		t.Errorf("an instance that is not there is nil, not an error: %+v %v", gone, err)
	}
	if (AWSInstance{PrivateIP: "192.0.2.9"}).Address() != "192.0.2.9" {
		t.Error("with no public address the private one is used")
	}

	for body, want := range map[string]string{
		`{"configured":false,"instances":[]}`:                                                      "AWS is not set up",
		`{"configured":true,"loaded":true,"error":"AWS could not be read: UnauthorizedOperation"}`: "could not read AWS",
	} {
		bad, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
		if _, err := bad.ListAWSInstances(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestWaitForAWSInstance(t *testing.T) {
	var polls int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		inst := ""
		switch {
		case n == 2:
			inst = `{"id":"i-0abc1234def567890","name":"web","region":"us-east-1","type":"t3.small","state":"pending"}`
		case n >= 3:
			inst = `{"id":"i-0abc1234def567890","name":"web","region":"us-east-1","type":"t3.small","state":"running","public_ip":"203.0.113.5"}`
		}
		w.Write([]byte(`{"configured":true,"loaded":true,"instances":[` + inst + `]}`))
	})
	in, err := c.WaitForAWSInstance("web", "us-east-1", 5*time.Second)
	if err != nil || in.ID != "i-0abc1234def567890" || in.Address() != "203.0.113.5" || atomic.LoadInt32(&polls) < 3 {
		t.Fatalf("wait = %+v, %v after %d polls", in, err, polls)
	}

	never, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"configured":true,"loaded":true,"instances":[]}`))
	})
	start := time.Now()
	if _, err := never.WaitForAWSInstance("web", "", 60*time.Millisecond); err == nil || !strings.Contains(err.Error(), "not running after") || time.Since(start) > 2*time.Second {
		t.Errorf("a timeout names what it saw: %v", err)
	}
	notSet, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"configured":false}`)) })
	start = time.Now()
	if _, err := notSet.WaitForAWSInstance("web", "", time.Minute); err == nil || !strings.Contains(err.Error(), "AWS is not set up") || time.Since(start) > time.Second {
		t.Errorf("without AWS it fails at once instead of waiting: %v", err)
	}
	// Another region's instance of the same name is not this one.
	other, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"configured":true,"loaded":true,"instances":[{"id":"i-0abc1234def567890","name":"web","region":"us-west-2","state":"running","public_ip":"203.0.113.5"}]}`))
	})
	if _, err := other.WaitForAWSInstance("web", "us-east-1", 40*time.Millisecond); err == nil {
		t.Error("an instance with the same name in another region must not be taken for this one")
	}
}

func TestTerminateAWSInstance(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	if err := c.TerminateAWSInstance("i-0abc1234def567890"); err != nil || rec.method != "DELETE" || rec.path != "/api/v1/fleet/aws/instances/i-0abc1234def567890" {
		t.Errorf("terminate = %v %+v", err, *rec)
	}
	if err := c.TerminateAWSInstance("web"); err == nil {
		t.Error("only an instance id is accepted, so a name can never terminate the wrong thing")
	}
	gone, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	if err := gone.TerminateAWSInstance("i-0abc1234def567890"); err != nil {
		t.Errorf("already gone counts as done: %v", err)
	}
	bad, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`{"error":"could not terminate: DependencyViolation","status":502}`))
	})
	if err := bad.TerminateAWSInstance("i-0abc1234def567890"); err == nil || !strings.Contains(err.Error(), "DependencyViolation") {
		t.Errorf("a failure is shown: %v", err)
	}
}

func TestAWSInstanceMapping(t *testing.T) {
	cost := 0.0208
	m := &AWSInstanceResourceModel{}
	mapAWSInstance(&AWSInstance{ID: "i-0abc1234def567890", Name: "web", Region: "us-east-1", Type: "t3.small", State: "running", PublicIP: "203.0.113.5",
		PrivateIP: "192.0.2.5", VCPUs: 2, HourlyUSD: &cost}, m)
	if m.ID.ValueString() != "i-0abc1234def567890" || m.IP.ValueString() != "203.0.113.5" || m.PrivateIP.ValueString() != "192.0.2.5" ||
		m.VCPUs.ValueInt64() != 2 || m.HourlyUSD.ValueFloat64() != 0.0208 || m.InstanceType.ValueString() != "t3.small" {
		t.Errorf("mapped = %+v", m)
	}
	mapAWSInstance(&AWSInstance{ID: "i-0abc1234def567890", PrivateIP: "192.0.2.5"}, m)
	if m.IP.ValueString() != "192.0.2.5" || m.HourlyUSD.ValueFloat64() != 0 {
		t.Errorf("with no public address or price: %+v", m)
	}
}

// An interrupted create leaves the name as the id. Destroy must still find and end the instance, and must
// never end the wrong one.
func TestAWSInstanceTerminateHandlesAnInterruptedCreate(t *testing.T) {
	var deleted []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deleted = append(deleted, r.URL.Path)
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Write([]byte(`{"configured":true,"loaded":true,"instances":[
			{"id":"i-0aaaaaaaaaaaaaaa1","name":"web","region":"us-east-1","state":"running","public_ip":"203.0.113.5"},
			{"id":"i-0bbbbbbbbbbbbbbb2","name":"web","region":"us-west-2","state":"running","public_ip":"203.0.113.6"},
			{"id":"i-0ccccccccccccccc3","name":"other","region":"us-east-1","state":"running"}]}`))
	})
	r := &AWSInstanceResource{client: c}
	model := func(id, name, region string) *AWSInstanceResourceModel {
		return &AWSInstanceResourceModel{ID: types.StringValue(id), Name: types.StringValue(name), Region: types.StringValue(region)}
	}

	if err := r.terminate(model("i-0ccccccccccccccc3", "other", "us-east-1")); err != nil || len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/i-0ccccccccccccccc3") {
		t.Errorf("a real id ends exactly that instance: %v %v", err, deleted)
	}
	deleted = nil
	if err := r.terminate(model("web", "web", "us-west-2")); err != nil || len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/i-0bbbbbbbbbbbbbbb2") {
		t.Errorf("an interrupted create is found by name and region, not by name alone: %v %v", err, deleted)
	}
	deleted = nil
	if err := r.terminate(model("ghost", "ghost", "us-east-1")); err != nil || len(deleted) != 0 {
		t.Errorf("an instance that never appeared leaves nothing to end: %v %v", err, deleted)
	}
	in, err := r.lookup(model("web", "web", "us-east-1"))
	if err != nil || in == nil || in.ID != "i-0aaaaaaaaaaaaaaa1" {
		t.Errorf("read finds an interrupted create by name and region: %+v %v", in, err)
	}
	if in, _ := r.lookup(model("i-0ddddddddddddddd4", "web", "us-east-1")); in != nil {
		t.Error("a recorded id that is gone reads as gone, so the resource is dropped from state")
	}
}

func TestAWSInstanceSchemaFixesEveryArgumentAndHidesThePassword(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewAWSInstanceResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatal(diags)
	}
	attrs := resp.Schema.Attributes
	for _, name := range []string{"name", "instance_type", "region", "os", "ssh_key", "user_script", "disk_gb", "ports"} {
		a, ok := attrs[name]
		if !ok {
			t.Errorf("missing attribute %s", name)
			continue
		}
		if strings.Contains(strings.ToLower(a.GetDescription()), "ttl") {
			t.Errorf("%s: no automatic termination in Terraform", name)
		}
	}
	if !attrs["password"].IsSensitive() {
		t.Error("the generated password must be sensitive")
	}
	if _, ok := attrs["ttl_hours"]; ok {
		t.Error("a self terminating instance would fight Terraform's state, so there is no ttl_hours")
	}
	if !strings.Contains(resp.Schema.Description, "costs money") || !strings.Contains(resp.Schema.Description, "terraform destroy") {
		t.Error("the resource description must say it costs money and how it ends")
	}
}

// With no disk_gb the request carries none, so the server keeps the image's own size.
func TestAWSLaunchWithoutADiskSendsNone(t *testing.T) {
	c, rec := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		w.Write([]byte(`{"name":"web","password":"pw","status":"creating","region":"us-east-1","instance_type":"t3.micro","estimated_hourly_usd":0.0104}`))
	})
	if _, err := c.LaunchAWSInstance(AWSLaunchRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.body, "disk_gb") {
		t.Errorf("no disk was asked for: %s", rec.body)
	}
}

func TestAWSInstanceDiskIsCheckedAtPlanTimeAndReplacesTheInstance(t *testing.T) {
	ctx := context.Background()
	resp := &resource.SchemaResponse{}
	NewAWSInstanceResource().Schema(ctx, resource.SchemaRequest{}, resp)
	attr, ok := resp.Schema.Attributes["disk_gb"]
	if !ok || !attr.IsOptional() || attr.IsComputed() {
		t.Fatalf("disk_gb should be optional and not computed: %+v", attr)
	}
	if !strings.Contains(attr.GetDescription(), "billed") || !strings.Contains(attr.GetDescription(), "8 to 2000") {
		t.Errorf("the description says the range and that it is billed: %s", attr.GetDescription())
	}

	check := func(disk types.Int64) bool {
		cfg := tfsdk.Config{Schema: resp.Schema, Raw: awsConfigWithDisk(t, resp.Schema, disk)}
		out := &resource.ValidateConfigResponse{}
		NewAWSInstanceResource().(resource.ResourceWithValidateConfig).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, out)
		return !out.Diagnostics.HasError()
	}
	for n, ok := range map[int64]bool{7: false, 8: true, 40: true, 2000: true, 2001: false, 0: false, -5: false} {
		if got := check(types.Int64Value(n)); got != ok {
			t.Errorf("disk_gb %d: accepted %v, want %v", n, got, ok)
		}
	}
	if !check(types.Int64Null()) {
		t.Error("leaving the disk out is fine")
	}
	if !check(types.Int64Unknown()) {
		t.Error("a value that is not known yet is checked when it is")
	}
}

// awsConfigWithDisk is a configuration in which every argument is unset except disk_gb.
func awsConfigWithDisk(t *testing.T, s schema.Schema, disk types.Int64) tftypes.Value {
	t.Helper()
	obj, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("the schema is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, typ := range obj.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, nil)
	}
	switch {
	case disk.IsUnknown():
		vals["disk_gb"] = tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	case !disk.IsNull():
		vals["disk_gb"] = tftypes.NewValue(tftypes.Number, disk.ValueInt64())
	}
	return tftypes.NewValue(obj, vals)
}

func TestLaunchRequestCarriesTheConfiguredDisk(t *testing.T) {
	plan := AWSInstanceResourceModel{
		Name: types.StringValue("web"), InstanceType: types.StringValue("t3.small"), OS: types.StringValue("ubuntu-24.04"),
		Region: types.StringUnknown(), DiskGB: types.Int64Value(40),
		Ports: []AWSPortModel{{Port: types.Int64Value(443), Source: types.StringValue("0.0.0.0/0")}},
	}
	req := launchRequestFromPlan(plan)
	if req.DiskGB != 40 || req.Name != "web" || req.InstanceType != "t3.small" || req.Region != "" || len(req.Ports) != 1 {
		t.Errorf("request = %+v", req)
	}
	plan.DiskGB = types.Int64Null()
	if launchRequestFromPlan(plan).DiskGB != 0 {
		t.Error("with no disk set none is requested")
	}
}
