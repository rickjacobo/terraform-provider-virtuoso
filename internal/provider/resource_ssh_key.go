package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = &SSHKeyResource{}
	_ resource.ResourceWithImportState = &SSHKeyResource{}
)

type SSHKeyResource struct {
	client *VirtuosoClient
}

type SSHKeyResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	PublicKey types.String `tfsdk:"public_key"`
	CreatedAt types.String `tfsdk:"created_at"`
}

func NewSSHKeyResource() resource.Resource {
	return &SSHKeyResource{}
}

func (r *SSHKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *SSHKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an SSH public key in virtuOSo.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "SSH key ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Name for the SSH key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_key": schema.StringAttribute{
				Description: "SSH public key content.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "Timestamp when the key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *SSHKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *SSHKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.CreateSSHKey(SSHKeyRequest{
		Name:      plan.Name.ValueString(),
		PublicKey: plan.PublicKey.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating SSH key", err.Error())
		return
	}

	plan.ID = types.StringValue(strconv.FormatInt(key.ID, 10))
	plan.CreatedAt = types.StringValue(key.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SSHKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys, err := r.client.ListSSHKeys()
	if err != nil {
		resp.Diagnostics.AddError("Error reading SSH keys", err.Error())
		return
	}

	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	var found *SSHKeyResponse
	for _, k := range keys {
		if k.ID == id {
			found = &k
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Name = types.StringValue(found.Name)
	state.PublicKey = types.StringValue(found.PublicKey)
	state.CreatedAt = types.StringValue(found.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SSHKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "SSH keys are immutable. Changes require replacement.")
}

func (r *SSHKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteSSHKey(id); err != nil {
		resp.Diagnostics.AddError("Error deleting SSH key", err.Error())
	}
}

func (r *SSHKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import by ID
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected numeric SSH key ID, got: %s", req.ID))
		return
	}

	keys, err := r.client.ListSSHKeys()
	if err != nil {
		resp.Diagnostics.AddError("Error listing SSH keys", err.Error())
		return
	}

	var found *SSHKeyResponse
	for _, k := range keys {
		if k.ID == id {
			found = &k
			break
		}
	}

	if found == nil {
		resp.Diagnostics.AddError("SSH key not found", fmt.Sprintf("No SSH key with ID %d", id))
		return
	}

	state := SSHKeyResourceModel{
		ID:        types.StringValue(strconv.FormatInt(found.ID, 10)),
		Name:      types.StringValue(found.Name),
		PublicKey: types.StringValue(found.PublicKey),
		CreatedAt: types.StringValue(found.CreatedAt),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
