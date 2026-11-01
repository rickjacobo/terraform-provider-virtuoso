package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &RestorePolicyResource{}
	_ resource.ResourceWithImportState    = &RestorePolicyResource{}
	_ resource.ResourceWithValidateConfig = &RestorePolicyResource{}
)

var retentionChoices = map[int64]bool{1: true, 3: true, 5: true, 7: true, 14: true, 28: true}

type RestorePolicyResource struct {
	client *VirtuosoClient
}

type RestorePolicyResourceModel struct {
	ID            types.String `tfsdk:"id"`
	VM            types.String `tfsdk:"vm"`
	Days          types.String `tfsdk:"days"`
	At            types.String `tfsdk:"at"`
	TZ            types.String `tfsdk:"tz"`
	RetentionDays types.Int64  `tfsdk:"retention_days"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	// Computed
	EstimatedCopies types.Int64  `tfsdk:"estimated_copies"`
	EstimatedBytes  types.Int64  `tfsdk:"estimated_bytes"`
	LastAt          types.String `tfsdk:"last_at"`
	LastResult      types.String `tfsdk:"last_result"`
	LastError       types.String `tfsdk:"last_error"`
}

func NewRestorePolicyResource() resource.Resource { return &RestorePolicyResource{} }

func (r *RestorePolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_restore_policy"
}

func (r *RestorePolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Takes an automatic snapshot of a VM at a set time on chosen days and deletes each one after retention_days. " +
			"A VM has no restore point policy until one is created. Each snapshot is a full copy, so long retention uses a lot of disk space: " +
			"see estimated_bytes. A run is skipped, with the reason in last_error, if disk space is short or the VM is busy. " +
			"Restoring a snapshot is not modelled in Terraform, because a restore is an action and not a state. Requires virtuOSo v0.7.0 or later.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The VM name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vm": schema.StringAttribute{
				Description:   "Name of the VM.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"days": schema.StringAttribute{
				Description: "everyday, weekdays, weekends, or a list such as mon,wed,fri.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("everyday"),
			},
			"at": schema.StringAttribute{
				Description: "Time of day, HH:MM on a 24 hour clock.",
				Required:    true,
			},
			"tz": schema.StringAttribute{
				Description: "IANA time zone. Empty uses the server's zone.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
			},
			"retention_days": schema.Int64Attribute{
				Description: "Days to keep each restore point: 1, 3, 5, 7, 14 or 28.",
				Required:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Set false to keep the policy but stop taking snapshots.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"estimated_copies": schema.Int64Attribute{
				Description: "How many automatic snapshots exist at once after a full retention period.",
				Computed:    true,
			},
			"estimated_bytes": schema.Int64Attribute{
				Description: "Roughly how much disk space those snapshots use in total, when the server can tell.",
				Computed:    true,
			},
			"last_at":     schema.StringAttribute{Description: "When the policy last ran (UTC).", Computed: true},
			"last_result": schema.StringAttribute{Description: "started or skipped.", Computed: true},
			"last_error":  schema.StringAttribute{Description: "Why the last run was skipped, if it was.", Computed: true},
		},
	}
}

func (r *RestorePolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateDays(ctx, req.Config, resp)
	var days types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("retention_days"), &days)...)
	if !days.IsNull() && !days.IsUnknown() && !retentionChoices[days.ValueInt64()] {
		resp.Diagnostics.AddAttributeError(path.Root("retention_days"), "Invalid retention",
			fmt.Sprintf("retention_days must be 1, 3, 5, 7, 14 or 28, got %d.", days.ValueInt64()))
	}
}

func (r *RestorePolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func mapRestorePolicy(p *RestorePolicy, est *RestorePolicyEstimate, m *RestorePolicyResourceModel) {
	m.ID = types.StringValue(p.VM)
	m.VM = types.StringValue(p.VM)
	if m.Days.IsNull() || m.Days.IsUnknown() || !sameDays(m.Days.ValueString(), p.Days) {
		m.Days = types.StringValue(p.Days)
	}
	m.At = types.StringValue(p.At)
	m.TZ = types.StringValue(p.TZ)
	m.RetentionDays = types.Int64Value(p.RetentionDays)
	m.Enabled = types.BoolValue(p.Enabled)
	m.LastAt = types.StringValue(p.LastAt)
	m.LastResult = types.StringValue(p.LastResult)
	m.LastError = types.StringValue(p.LastError)
	if est != nil {
		m.EstimatedCopies = types.Int64Value(est.Copies)
		m.EstimatedBytes = types.Int64Value(est.Bytes)
	} else {
		m.EstimatedCopies = types.Int64Value(0)
		m.EstimatedBytes = types.Int64Value(0)
	}
}

func (r *RestorePolicyResource) save(ctx context.Context, plan *RestorePolicyResourceModel) error {
	tflog.Info(ctx, "Saving restore point policy", map[string]interface{}{"vm": plan.VM.ValueString()})
	saved, est, err := r.client.PutRestorePolicy(RestorePolicy{
		VM: plan.VM.ValueString(), Days: plan.Days.ValueString(), At: plan.At.ValueString(), TZ: plan.TZ.ValueString(),
		RetentionDays: plan.RetentionDays.ValueInt64(), Enabled: plan.Enabled.ValueBool(),
	})
	if err != nil {
		return err
	}
	mapRestorePolicy(saved, est, plan)
	return nil
}

func (r *RestorePolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RestorePolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.save(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error saving restore point policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RestorePolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RestorePolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, est, err := r.client.GetRestorePolicy(state.VM.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading restore point policy", err.Error())
		return
	}
	if p == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapRestorePolicy(p, est, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RestorePolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RestorePolicyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.save(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error saving restore point policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RestorePolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RestorePolicyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteRestorePolicy(state.VM.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error removing restore point policy", err.Error())
	}
}

func (r *RestorePolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p, est, err := r.client.GetRestorePolicy(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing restore point policy", err.Error())
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("Policy not found", "VM "+req.ID+" has no restore point policy.")
		return
	}
	var m RestorePolicyResourceModel
	mapRestorePolicy(p, est, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
