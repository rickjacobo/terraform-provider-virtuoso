package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &VPCPeeringResource{}
	_ resource.ResourceWithImportState    = &VPCPeeringResource{}
	_ resource.ResourceWithValidateConfig = &VPCPeeringResource{}
)

const peeringSameVPCMsg = "A VPC cannot be peered with itself. Set vpc_a and vpc_b to two different VPCs."

type VPCPeeringResource struct {
	client *VirtuosoClient
}

type VPCPeeringResourceModel struct {
	ID    types.String `tfsdk:"id"`
	VPCA  types.String `tfsdk:"vpc_a"`
	VPCB  types.String `tfsdk:"vpc_b"`
	CIDRA types.String `tfsdk:"cidr_a"`
	CIDRB types.String `tfsdk:"cidr_b"`
}

func NewVPCPeeringResource() resource.Resource { return &VPCPeeringResource{} }

func (r *VPCPeeringResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_peering"
}

func (r *VPCPeeringResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a peering between two VPCs. A peering opens the way only: nothing crosses it until security group rules in each VPC allow it. The receiving side needs an ingress rule that names the other VPC's cidr, and the sending side needs an egress rule. Rules are directional, so one side can serve a port that the other side cannot reach back. Peering is not transitive: if A is peered with B and B with C, A cannot reach C. The two ranges must not overlap. Needs an admin API key. Changing either VPC replaces the peering.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The two VPC names joined by a slash, in the order they are written in the configuration: vpc_a/vpc_b.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc_a": schema.StringAttribute{
				Description:   "The name of one VPC. The server stores a peering with its two names sorted, but the state keeps the order written here. Must differ from vpc_b. Changing it replaces the peering.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vpc_b": schema.StringAttribute{
				Description:   "The name of the other VPC. Must differ from vpc_a. Its address range must not overlap the range of vpc_a. Changing it replaces the peering.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"cidr_a": schema.StringAttribute{
				Description:   "The address range of vpc_a. Name it in a security group rule of vpc_b to allow traffic from vpc_a.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"cidr_b": schema.StringAttribute{
				Description:   "The address range of vpc_b. Name it in a security group rule of vpc_a to allow traffic from vpc_b.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *VPCPeeringResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c VPCPeeringResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if c.VPCA.IsUnknown() || c.VPCB.IsUnknown() || c.VPCA.IsNull() || c.VPCB.IsNull() {
		return
	}
	if c.VPCA.ValueString() == c.VPCB.ValueString() {
		resp.Diagnostics.AddError("Cannot peer a VPC with itself", peeringSameVPCMsg)
	}
}

func (r *VPCPeeringResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// fill sets the ranges from the API record, mapping its sorted names back to the order in the model, which
// stays as the user wrote it.
func (m *VPCPeeringResourceModel) fill(p *PeeringResponse) {
	m.ID = types.StringValue(m.VPCA.ValueString() + "/" + m.VPCB.ValueString())
	if p.VPCA == m.VPCA.ValueString() {
		m.CIDRA, m.CIDRB = types.StringValue(p.CIDRA), types.StringValue(p.CIDRB)
	} else {
		m.CIDRA, m.CIDRB = types.StringValue(p.CIDRB), types.StringValue(p.CIDRA)
	}
}

func (r *VPCPeeringResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCPeeringResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, b := plan.VPCA.ValueString(), plan.VPCB.ValueString()
	if a == b {
		resp.Diagnostics.AddError("Cannot peer a VPC with itself", peeringSameVPCMsg)
		return
	}
	p, err := r.client.CreatePeering(a, b)
	if err != nil {
		resp.Diagnostics.AddError("Error creating VPC peering", err.Error())
		return
	}
	plan.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCPeeringResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCPeeringResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.GetPeering(state.VPCA.ValueString(), state.VPCB.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VPC peering", err.Error())
		return
	}
	if p == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never reached: both names require a replace, and the computed values do not change.
func (r *VPCPeeringResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VPCPeeringResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VPCPeeringResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCPeeringResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeletePeering(state.VPCA.ValueString(), state.VPCB.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error ending VPC peering", err.Error())
	}
}

// ImportState takes <vpc_a>/<vpc_b>, in either order. The state keeps the order given, so import with the
// order the configuration uses.
func (r *VPCPeeringResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	a, b, ok := strings.Cut(req.ID, "/")
	if !ok || a == "" || b == "" || strings.Contains(b, "/") {
		resp.Diagnostics.AddError("Invalid import ID", "Use <vpc_a>/<vpc_b>, the names of the two peered VPCs, for example app/backend.")
		return
	}
	if a == b {
		resp.Diagnostics.AddError("Invalid import ID", peeringSameVPCMsg)
		return
	}
	p, err := r.client.GetPeering(a, b)
	if err != nil {
		resp.Diagnostics.AddError("Error importing VPC peering", err.Error())
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("VPC peering not found", a+" and "+b+" are not peered")
		return
	}
	state := VPCPeeringResourceModel{VPCA: types.StringValue(a), VPCB: types.StringValue(b)}
	state.fill(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
