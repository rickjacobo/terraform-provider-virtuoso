package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// tailscaleAttribute is the optional "tailscale" argument of every resource that launches a VM. Left out, the
// server decides as before: the VM joins Tailscale when a key is configured. Only false is ever sent.
func tailscaleAttribute(what string, extra ...string) schema.BoolAttribute {
	more := ""
	for _, e := range extra {
		more += " " + e
	}
	return schema.BoolAttribute{
		Description: "Whether the " + what + " joins your Tailscale network when the server has a Tailscale key. Defaults to true. " +
			"Set it to false to keep it off the tailnet, for example to stay under the 50 device limit of Tailscale's free plan. " +
			"It only matters at launch and cannot be undone later, so changing it replaces the " + what + ". " +
			"An imported resource cannot tell, so the value in the configuration is kept without replacing anything." + more,
		Optional: true,
		PlanModifiers: []planmodifier.Bool{
			tailscaleReplace{},
		},
	}
}

// tailscaleOptOut is what goes into the launch request: nil unless the configuration says false, so the field
// is left out of the request and the server's own default applies.
func tailscaleOptOut(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() || v.ValueBool() {
		return nil
	}
	off := false
	return &off
}

// tailscaleReplace replaces the resource when the choice really changes. Left out and true mean the same, so
// moving between them is not a change, and a null state (an imported resource, or one made before this
// argument existed) is never replaced, because nothing is known about how it was launched.
type tailscaleReplace struct{}

func (tailscaleReplace) Description(context.Context) string {
	return "Replaces the resource when tailscale changes between true and false."
}

func (m tailscaleReplace) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (tailscaleReplace) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.State.Raw.IsNull() || req.StateValue.IsNull() || req.PlanValue.IsUnknown() {
		return // being created, imported or not known yet
	}
	joins := func(v types.Bool) bool { return v.IsNull() || v.ValueBool() }
	if joins(req.StateValue) != joins(req.PlanValue) {
		resp.RequiresReplace = true
	}
}
