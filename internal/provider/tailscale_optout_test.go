package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestTailscaleAttributeIsOptionalWithoutADefault(t *testing.T) {
	a, ok := vmSchema(t).Schema.Attributes["tailscale"]
	if !ok || !a.IsOptional() || a.IsComputed() || a.IsRequired() {
		t.Fatal("tailscale must be optional and not computed, so leaving it out plans no change")
	}
	for _, r := range []resource.Resource{NewAWSInstanceResource(), NewK8sClusterResource(), NewDockerInstanceResource()} {
		var resp resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
		a, ok := resp.Schema.Attributes["tailscale"]
		if !ok || !a.IsOptional() || a.IsComputed() {
			t.Errorf("%T has no optional tailscale attribute", r)
		}
		if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Error(diags)
		}
	}
}

func TestTailscaleOptOutIsSentOnlyWhenFalse(t *testing.T) {
	for name, c := range map[string]struct {
		in   types.Bool
		sent bool
	}{"null": {types.BoolNull(), false}, "unknown": {types.BoolUnknown(), false}, "true": {types.BoolValue(true), false}, "false": {types.BoolValue(false), true}} {
		body := []map[string]any{}
		for _, req := range []any{
			LaunchRequest{Name: "a", Tailscale: tailscaleOptOut(c.in)},
			FleetLaunchRequest{LaunchRequest: LaunchRequest{Name: "a", Tailscale: tailscaleOptOut(c.in)}, Node: "lab-02"},
			AWSLaunchRequest{Name: "a", Tailscale: tailscaleOptOut(c.in)},
			K8sCreateRequest{Name: "a", Tailscale: tailscaleOptOut(c.in)},
			DockerCreateRequest{Name: "a", Tailscale: tailscaleOptOut(c.in)},
		} {
			raw, _ := json.Marshal(req)
			m := map[string]any{}
			json.Unmarshal(raw, &m)
			body = append(body, m)
		}
		for i, m := range body {
			v, has := m["tailscale"]
			if has != c.sent || (has && v != false) {
				t.Errorf("%s, request %d: tailscale = %v (present %v), want sent=%v", name, i, v, has, c.sent)
			}
		}
	}
}

func TestVMCreateSendsTailscaleFalseOnly(t *testing.T) {
	for name, c := range map[string]struct {
		in   types.Bool
		sent bool
	}{"false": {types.BoolValue(false), true}, "true": {types.BoolValue(true), false}, "omitted": {types.BoolNull(), false}} {
		cl, f := newTagServer(t, nil)
		r := &VMResource{client: cl}
		plan := baseModel(types.StringUnknown())
		plan.Tailscale = c.in
		resp := resource.CreateResponse{State: emptyState(t)}
		r.Create(context.Background(), resource.CreateRequest{Plan: asPlan(t, plan)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", name, resp.Diagnostics)
		}
		v, has := f.launch["tailscale"]
		if has != c.sent || (has && v != false) {
			t.Errorf("%s: the launch carried %v", name, f.launch)
		}
		// Plan after apply shows no change: the state keeps exactly what the configuration said.
		var got VMResourceModel
		resp.State.Get(context.Background(), &got)
		if !got.Tailscale.Equal(c.in) {
			t.Errorf("%s: state tailscale = %v, want %v", name, got.Tailscale, c.in)
		}
	}
}

func TestTailscaleReplaceOnlyWhenTheChoiceChanges(t *testing.T) {
	ctx := context.Background()
	boolType := tftypes.Bool
	raw := func(v types.Bool) tftypes.Value {
		switch {
		case v.IsNull():
			return tftypes.NewValue(boolType, nil)
		default:
			return tftypes.NewValue(boolType, v.ValueBool())
		}
	}
	T, F, N := types.BoolValue(true), types.BoolValue(false), types.BoolNull()
	cases := []struct {
		name        string
		state, plan types.Bool
		creating    bool
		wantReplace bool
	}{
		{"create", N, F, true, false},
		{"true to false", T, F, false, true},
		{"false to true", F, T, false, true},
		{"false to omitted", F, N, false, true},
		{"omitted to false, as after an import", N, F, false, false},
		{"omitted to true", N, T, false, false},
		{"true to omitted means the same", T, N, false, false},
		{"unchanged false", F, F, false, false},
	}
	for _, c := range cases {
		req := planmodifier.BoolRequest{StateValue: c.state, PlanValue: c.plan}
		req.State.Raw = tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"tailscale": boolType}}, map[string]tftypes.Value{"tailscale": raw(c.state)})
		if c.creating {
			req.State.Raw = tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"tailscale": boolType}}, nil)
		}
		resp := &planmodifier.BoolResponse{PlanValue: c.plan}
		tailscaleReplace{}.PlanModifyBool(ctx, req, resp)
		if resp.RequiresReplace != c.wantReplace {
			t.Errorf("%s: RequiresReplace = %v, want %v", c.name, resp.RequiresReplace, c.wantReplace)
		}
	}
	if !strings.Contains(tailscaleReplace{}.Description(ctx), "Replaces") {
		t.Error("the modifier has no description")
	}
}

func TestVMImportLeavesTailscaleNull(t *testing.T) {
	cl, _ := newTagServer(t, nil)
	r := &VMResource{client: cl}
	resp := resource.ImportStateResponse{State: emptyState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "web1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var got VMResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Tailscale.IsNull() {
		t.Errorf("an imported VM cannot know how it was launched, got %v", got.Tailscale)
	}
}
