package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VPCResource{}
	_ resource.ResourceWithImportState = &VPCResource{}
)

type VPCResource struct {
	client *VirtuosoClient
}

type VPCResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	CIDR      types.String `tfsdk:"cidr"`
	MTU       types.Int64  `tfsdk:"mtu"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func NewVPCResource() resource.Resource { return &VPCResource{} }

func (r *VPCResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *VPCResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC: a private network that VMs join when they are launched. Needs an admin API key. A VPC can only be deleted when no VM is in it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The VPC name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "VPC name: lowercase letters, digits and dashes, starting with a letter.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cidr": schema.StringAttribute{
				Description:   "The address range of the VPC, for example 10.20.0.0/16. It must not overlap another VPC or the server's own networks. Changing it replaces the VPC.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"mtu": schema.Int64Attribute{
				Description: "The MTU of the VPC network. When left out, the server's default is used. Changing it replaces the VPC.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				Description:   "When the VPC was created.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *VPCResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (m *VPCResourceModel) fill(v *VPCResponse) {
	m.ID = types.StringValue(v.Name)
	m.Name = types.StringValue(v.Name)
	m.CIDR = types.StringValue(v.CIDR)
	m.MTU = types.Int64Value(v.MTU)
	m.CreatedAt = types.StringValue(v.CreatedAt)
}

func (r *VPCResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var mtu int64
	if !plan.MTU.IsNull() && !plan.MTU.IsUnknown() {
		mtu = plan.MTU.ValueInt64()
	}
	v, err := r.client.CreateVPC(plan.Name.ValueString(), plan.CIDR.ValueString(), mtu)
	if err != nil {
		resp.Diagnostics.AddError("Error creating VPC", err.Error())
		return
	}
	plan.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.GetVPC(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VPC", err.Error())
		return
	}
	if v == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VPCResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "A VPC cannot be changed in place. Changes require replacement.")
}

func (r *VPCResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteVPC(state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting VPC", err.Error())
	}
}

func (r *VPCResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	v, err := r.client.GetVPC(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing VPC", err.Error())
		return
	}
	if v == nil {
		resp.Diagnostics.AddError("VPC not found", "No VPC named "+req.ID)
		return
	}
	var state VPCResourceModel
	state.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
