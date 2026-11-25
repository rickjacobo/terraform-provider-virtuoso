package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &FloatingIPResource{}
	_ resource.ResourceWithImportState    = &FloatingIPResource{}
	_ resource.ResourceWithValidateConfig = &FloatingIPResource{}
)

func pathRoot(name string) path.Path { return path.Root(name) }

type FloatingIPResource struct {
	client *VirtuosoClient
}

type FloatingIPResourceModel struct {
	ID      types.String `tfsdk:"id"`
	Address types.String `tfsdk:"address"`
	VPC     types.String `tfsdk:"vpc"`
	VM      types.String `tfsdk:"vm"`
}

func NewFloatingIPResource() resource.Resource { return &FloatingIPResource{} }

func (r *FloatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ip"
}

func (r *FloatingIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a floating IP: an address from the server's floating IP pool that can be pointed at a VM in a VPC. The pool is set in the VPC network settings. Needs an admin API key. Set vpc and vm together to attach it; leave both out to keep the address unattached.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The address.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"address": schema.StringAttribute{
				Description: "The floating IP. When left out, the next free address in the pool is used. Changing it replaces the resource.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc": schema.StringAttribute{
				Description: "The VPC of the VM the address points at. Set together with vm.",
				Optional:    true,
			},
			"vm": schema.StringAttribute{
				Description: "The VM the address points at. Set together with vpc.",
				Optional:    true,
			},
		},
	}
}

func (r *FloatingIPResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c FloatingIPResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if c.VPC.IsUnknown() || c.VM.IsUnknown() {
		return
	}
	if c.VPC.IsNull() != c.VM.IsNull() {
		resp.Diagnostics.AddError("Set vpc and vm together", "A floating IP is attached with both vpc and vm, or left unattached with neither.")
	}
}

func (r *FloatingIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*VirtuosoClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *VirtuosoClient")
		return
	}
	r.client = client
}

func (r *FloatingIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FloatingIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var want string
	if !plan.Address.IsNull() && !plan.Address.IsUnknown() {
		want = plan.Address.ValueString()
	}
	// An address the config names that is already allocated but attached to nothing is a leftover (an earlier
	// run stopped half way). Take it over instead of failing with "already allocated".
	var f *FloatingIPResponse
	var err error
	if want != "" {
		if have, gerr := r.client.GetFloatingIP(want); gerr == nil && have != nil && have.VM == "" {
			f = have
		}
	}
	if f == nil {
		f, err = r.client.AllocateFloatingIP(want)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error allocating floating IP", err.Error())
		return
	}
	plan.Address, plan.ID = types.StringValue(f.Address), types.StringValue(f.Address)
	if !plan.VM.IsNull() {
		if err := r.client.AttachFloatingIP(f.Address, plan.VPC.ValueString(), plan.VM.ValueString()); err != nil {
			// Do not leave an address allocated that the state does not know about.
			if rerr := r.client.ReleaseFloatingIP(f.Address); rerr != nil {
				resp.Diagnostics.AddWarning("Could not release the address", f.Address+": "+rerr.Error())
			}
			resp.Diagnostics.AddError("Error attaching floating IP", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FloatingIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FloatingIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f, err := r.client.GetFloatingIP(state.Address.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading floating IP", err.Error())
		return
	}
	if f == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (m *FloatingIPResourceModel) fill(f *FloatingIPResponse) {
	m.ID, m.Address = types.StringValue(f.Address), types.StringValue(f.Address)
	m.VPC, m.VM = types.StringNull(), types.StringNull()
	if f.VM != "" {
		m.VPC, m.VM = types.StringValue(f.VPC), types.StringValue(f.VM)
	}
}

func (r *FloatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state FloatingIPResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	addr := state.Address.ValueString()
	if !plan.VPC.Equal(state.VPC) || !plan.VM.Equal(state.VM) {
		if !state.VM.IsNull() {
			if err := r.client.DetachFloatingIP(addr); err != nil {
				resp.Diagnostics.AddError("Error detaching floating IP", err.Error())
				return
			}
		}
		if !plan.VM.IsNull() {
			if err := r.client.AttachFloatingIP(addr, plan.VPC.ValueString(), plan.VM.ValueString()); err != nil {
				state.VPC, state.VM = types.StringNull(), types.StringNull()
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				resp.Diagnostics.AddError("Error attaching floating IP", err.Error())
				return
			}
		}
	}
	plan.Address, plan.ID = state.Address, state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FloatingIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FloatingIPResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	addr := state.Address.ValueString()
	// Always detach first: the state can be older than the server (a VM replaced or deleted since the last
	// refresh), and the server refuses to release an address that is still attached. Detaching one that is
	// not attached, or not known, does nothing.
	if err := r.client.DetachFloatingIP(addr); err != nil {
		resp.Diagnostics.AddError("Error detaching floating IP", err.Error())
		return
	}
	if err := r.client.ReleaseFloatingIP(addr); err != nil {
		resp.Diagnostics.AddError("Error releasing floating IP", err.Error())
	}
}

// ImportState takes the address.
func (r *FloatingIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	f, err := r.client.GetFloatingIP(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing floating IP", err.Error())
		return
	}
	if f == nil {
		resp.Diagnostics.AddError("Floating IP not found", req.ID+" is not allocated")
		return
	}
	var state FloatingIPResourceModel
	state.fill(f)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
