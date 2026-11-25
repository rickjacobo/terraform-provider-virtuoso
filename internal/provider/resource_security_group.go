package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &SecurityGroupResource{}
	_ resource.ResourceWithImportState = &SecurityGroupResource{}
)

type SecurityGroupResource struct {
	client *VirtuosoClient
}

type SecurityGroupResourceModel struct {
	ID          types.String `tfsdk:"id"`
	VPC         types.String `tfsdk:"vpc"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func NewSecurityGroupResource() resource.Resource { return &SecurityGroupResource{} }

func (r *SecurityGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (r *SecurityGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a security group in a VPC. A new group allows nothing until rules are added with virtuoso_security_group_rule. Every VPC already has a group called default. A group that a VM is in cannot be deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The VPC and group name, as vpc/group.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc": schema.StringAttribute{
				Description:   "Name of the VPC the group belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description:   "Group name: 1 to 25 lowercase letters, digits or dashes, starting with a letter.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Description:   "Up to 120 characters.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(""),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
		},
	}
}

func (r *SecurityGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *SecurityGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecurityGroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpc, name := plan.VPC.ValueString(), plan.Name.ValueString()
	if err := r.client.CreateSecurityGroup(vpc, name, plan.Description.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error creating security group", err.Error())
		return
	}
	plan.ID = types.StringValue(vpc + "/" + name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SecurityGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecurityGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.GetSecurityGroup(state.VPC.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	if g == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Description = types.StringValue(g.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SecurityGroupResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "A security group cannot be changed in place. Changes require replacement.")
}

func (r *SecurityGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecurityGroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSecurityGroup(state.VPC.ValueString(), state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting security group", err.Error())
	}
}

// ImportState takes vpc/group.
func (r *SecurityGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpc, name, ok := strings.Cut(req.ID, "/")
	if !ok || vpc == "" || name == "" || strings.Contains(name, "/") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected vpc/group, got: %s", req.ID))
		return
	}
	g, err := r.client.GetSecurityGroup(vpc, name)
	if err != nil {
		resp.Diagnostics.AddError("Error importing security group", err.Error())
		return
	}
	if g == nil {
		resp.Diagnostics.AddError("Security group not found", "No group "+req.ID)
		return
	}
	state := SecurityGroupResourceModel{
		ID: types.StringValue(req.ID), VPC: types.StringValue(vpc), Name: types.StringValue(name),
		Description: types.StringValue(g.Description),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
