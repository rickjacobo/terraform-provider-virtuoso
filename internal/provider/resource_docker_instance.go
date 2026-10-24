package provider

import (
	"context"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &DockerInstanceResource{}
	_ resource.ResourceWithImportState = &DockerInstanceResource{}
)

const dockerReadyTimeout = 20 * time.Minute

type DockerInstanceResource struct {
	client *VirtuosoClient
}

type DockerInstanceResourceModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Size    types.String `tfsdk:"size"`
	DiskGB  types.Int64  `tfsdk:"disk_gb"`
	Bridged types.Bool   `tfsdk:"bridged"`
	// Computed
	VMName      types.String `tfsdk:"vm_name"`
	Status      types.String `tfsdk:"status"`
	IP          types.String `tfsdk:"ip"`
	TailscaleIP types.String `tfsdk:"tailscale_ip"`
}

func NewDockerInstanceResource() resource.Resource {
	return &DockerInstanceResource{}
}

func (r *DockerInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_docker_instance"
}

func (r *DockerInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Docker instance (the Docker app): a VM with Docker and Compose installed. Requires an admin API key. " +
			"All arguments are fixed at creation; changing one recreates the instance. Log in to the VM with the virtuoso_vm data source (vm_name).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Instance name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Instance name. The VM is named docker-<name>.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"size": schema.StringAttribute{
				Description:   "Instance size, for example medium or large.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("medium"),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"disk_gb": schema.Int64Attribute{
				Description: "Disk size in GB. 0 uses the size's default.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"bridged": schema.BoolAttribute{
				Description:   "Use bridged networking instead of NAT (on-prem only).",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"vm_name": schema.StringAttribute{
				Description:   "Name of the instance's VM.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Description: "provisioning or running.",
				Computed:    true,
			},
			"ip": schema.StringAttribute{
				Description:   "IP address of the instance.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tailscale_ip": schema.StringAttribute{
				Description: "Tailscale IP, when Tailscale is configured on the server.",
				Computed:    true,
			},
		},
	}
}

func (r *DockerInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func mapDockerInstance(inst *AppInstanceResponse, m *DockerInstanceResourceModel) {
	m.ID = types.StringValue(inst.Name)
	m.Name = types.StringValue(inst.Name)
	m.VMName = types.StringValue(inst.VMName)
	m.Status = types.StringValue(inst.Status)
	m.IP = types.StringValue(inst.IP)
	m.TailscaleIP = types.StringValue(inst.TailscaleIP)
	m.Size = types.StringValue(inst.Size)
	m.DiskGB = types.Int64Value(inst.DiskGB)
}

func (r *DockerInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DockerInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	create := DockerCreateRequest{
		Name:    plan.Name.ValueString(),
		Size:    plan.Size.ValueString(),
		Bridged: plan.Bridged.ValueBool(),
	}
	if !plan.DiskGB.IsNull() && !plan.DiskGB.IsUnknown() {
		create.DiskGB = plan.DiskGB.ValueInt64()
	}

	tflog.Info(ctx, "Creating Docker instance", map[string]interface{}{"name": create.Name})
	inst, err := r.client.CreateDockerInstance(create)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Docker instance", err.Error())
		return
	}

	// Track it in state before waiting so a timeout leaves it tainted, not orphaned.
	mapDockerInstance(inst, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ready, err := r.client.WaitForDockerInstance(plan.Name.ValueString(), dockerReadyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Docker instance", err.Error())
		return
	}
	mapDockerInstance(ready, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DockerInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DockerInstanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, err := r.client.GetDockerInstance(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading Docker instance", err.Error())
		return
	}
	if inst == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapDockerInstance(inst, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Every argument forces replacement, so Update only ever sees computed refreshes.
func (r *DockerInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DockerInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, err := r.client.GetDockerInstance(plan.Name.ValueString())
	if err != nil || inst == nil {
		resp.Diagnostics.AddError("Error reading Docker instance", "instance not found or unreadable")
		return
	}
	mapDockerInstance(inst, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DockerInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DockerInstanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Info(ctx, "Deleting Docker instance", map[string]interface{}{"name": state.Name.ValueString()})
	if err := r.client.DeleteDockerInstance(state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting Docker instance", err.Error())
	}
}

func (r *DockerInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	inst, err := r.client.GetDockerInstance(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Docker instance", err.Error())
		return
	}
	if inst == nil {
		resp.Diagnostics.AddError("Instance not found", "No Docker instance named "+req.ID)
		return
	}
	state := DockerInstanceResourceModel{Bridged: types.BoolValue(false)}
	mapDockerInstance(inst, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
