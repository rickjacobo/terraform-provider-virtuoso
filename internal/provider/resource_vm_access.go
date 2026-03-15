package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VMAccessResource{}
	_ resource.ResourceWithImportState = &VMAccessResource{}
)

type VMAccessResource struct {
	client *VirtuosoClient
}

type VMAccessResourceModel struct {
	ID     types.String `tfsdk:"id"`
	VMName types.String `tfsdk:"vm_name"`
	UserID types.Int64  `tfsdk:"user_id"`
}

func NewVMAccessResource() resource.Resource {
	return &VMAccessResource{}
}

func (r *VMAccessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_access"
}

func (r *VMAccessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Grants a user access to a virtuOSo VM. Admin-only.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Synthetic ID: {vm_name}/{user_id}.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vm_name": schema.StringAttribute{
				Description: "VM name to grant access to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_id": schema.Int64Attribute{
				Description: "User ID to grant access.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *VMAccessResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VMAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMAccessResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vmName := plan.VMName.ValueString()
	userID := plan.UserID.ValueInt64()

	if err := r.client.GrantVMAccess(vmName, userID); err != nil {
		resp.Diagnostics.AddError("Error granting VM access", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%d", vmName, userID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vmName := state.VMName.ValueString()
	userID := state.UserID.ValueInt64()

	entries, err := r.client.ListVMAccess(vmName)
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM access", err.Error())
		return
	}

	found := false
	for _, e := range entries {
		if e.ID == userID {
			found = true
			break
		}
	}

	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(fmt.Sprintf("%s/%d", vmName, userID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VMAccessResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "VM access grants are immutable. Changes require replacement.")
}

func (r *VMAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMAccessResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.RevokeVMAccess(state.VMName.ValueString(), state.UserID.ValueInt64()); err != nil {
		resp.Diagnostics.AddError("Error revoking VM access", err.Error())
	}
}

func (r *VMAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid import ID", "Expected format: {vm_name}/{user_id}")
		return
	}

	vmName := parts[0]
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid user_id in import ID", err.Error())
		return
	}

	state := VMAccessResourceModel{
		ID:     types.StringValue(req.ID),
		VMName: types.StringValue(vmName),
		UserID: types.Int64Value(userID),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
