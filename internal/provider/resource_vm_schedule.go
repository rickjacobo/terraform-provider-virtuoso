package provider

import (
	"context"

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
	_ resource.Resource                   = &VMScheduleResource{}
	_ resource.ResourceWithImportState    = &VMScheduleResource{}
	_ resource.ResourceWithValidateConfig = &VMScheduleResource{}
)

type VMScheduleResource struct {
	client *VirtuosoClient
}

type VMScheduleResourceModel struct {
	ID      types.String `tfsdk:"id"`
	VM      types.String `tfsdk:"vm"`
	Days    types.String `tfsdk:"days"`
	StartAt types.String `tfsdk:"start_at"`
	StopAt  types.String `tfsdk:"stop_at"`
	TZ      types.String `tfsdk:"tz"`
	Enabled types.Bool   `tfsdk:"enabled"`
	// Computed
	LastAction types.String `tfsdk:"last_action"`
	LastAt     types.String `tfsdk:"last_at"`
	LastError  types.String `tfsdk:"last_error"`
}

func NewVMScheduleResource() resource.Resource { return &VMScheduleResource{} }

func (r *VMScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_schedule"
}

func (r *VMScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Starts and stops a VM automatically at set times. A VM has no schedule until one is created. " +
			"Each time applies on the days listed, in the time zone given. A start or stop missed while virtuOSo was down is skipped. " +
			"Requires a virtuOSo server that supports schedules (v0.7.0 or later).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The VM name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vm": schema.StringAttribute{
				Description:   "Name of the VM to schedule.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"days": schema.StringAttribute{
				Description: "everyday, weekdays, weekends, or a list such as mon,wed,fri.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("everyday"),
			},
			"start_at": schema.StringAttribute{
				Description: "Start time, HH:MM on a 24 hour clock. Set start_at, stop_at, or both.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
			},
			"stop_at": schema.StringAttribute{
				Description: "Stop time, HH:MM on a 24 hour clock.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
			},
			"tz": schema.StringAttribute{
				Description: "IANA time zone such as America/New_York. Empty uses the server's zone.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
			},
			"enabled": schema.BoolAttribute{
				Description: "Set false to keep the times but turn the schedule off.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"last_action": schema.StringAttribute{Description: "The last action taken, start or stop.", Computed: true},
			"last_at":     schema.StringAttribute{Description: "When the last action ran (UTC).", Computed: true},
			"last_error":  schema.StringAttribute{Description: "Why the last action failed, if it did.", Computed: true},
		},
	}
}

func (r *VMScheduleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	validateDays(ctx, req.Config, resp)
}

func (r *VMScheduleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// mapSchedule copies the server's schedule into the model. The days value from the
// plan is kept when it means the same as the server's, so spelling differences such
// as "Mon, Fri" and "mon,fri" are not reported as changes.
func mapSchedule(s *VMSchedule, m *VMScheduleResourceModel) {
	m.ID = types.StringValue(s.VM)
	m.VM = types.StringValue(s.VM)
	if m.Days.IsNull() || m.Days.IsUnknown() || !sameDays(m.Days.ValueString(), s.Days) {
		m.Days = types.StringValue(s.Days)
	}
	m.StartAt = types.StringValue(s.StartAt)
	m.StopAt = types.StringValue(s.StopAt)
	m.TZ = types.StringValue(s.TZ)
	m.Enabled = types.BoolValue(s.Enabled)
	m.LastAction = types.StringValue(s.LastAction)
	m.LastAt = types.StringValue(s.LastAt)
	m.LastError = types.StringValue(s.LastError)
}

func scheduleFromModel(m VMScheduleResourceModel) VMSchedule {
	return VMSchedule{VM: m.VM.ValueString(), Days: m.Days.ValueString(), StartAt: m.StartAt.ValueString(),
		StopAt: m.StopAt.ValueString(), TZ: m.TZ.ValueString(), Enabled: m.Enabled.ValueBool()}
}

func (r *VMScheduleResource) save(ctx context.Context, plan *VMScheduleResourceModel) error {
	tflog.Info(ctx, "Saving VM schedule", map[string]interface{}{"vm": plan.VM.ValueString()})
	saved, err := r.client.PutSchedule(scheduleFromModel(*plan))
	if err != nil {
		return err
	}
	mapSchedule(saved, plan)
	return nil
}

func (r *VMScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMScheduleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.save(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error saving VM schedule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMScheduleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.GetSchedule(state.VM.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM schedule", err.Error())
		return
	}
	if s == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapSchedule(s, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VMScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VMScheduleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.save(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Error saving VM schedule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMScheduleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSchedule(state.VM.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error removing VM schedule", err.Error())
	}
}

// ImportState imports an existing schedule by VM name.
func (r *VMScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	s, err := r.client.GetSchedule(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing VM schedule", err.Error())
		return
	}
	if s == nil {
		resp.Diagnostics.AddError("Schedule not found", "VM "+req.ID+" has no schedule.")
		return
	}
	var m VMScheduleResourceModel
	mapSchedule(s, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), s.VM)...)
}
