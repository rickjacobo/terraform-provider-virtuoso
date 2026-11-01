package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &SnapshotResource{}
	_ resource.ResourceWithImportState    = &SnapshotResource{}
	_ resource.ResourceWithValidateConfig = &SnapshotResource{}
)

const snapshotReadyTimeout = 45 * time.Minute

type SnapshotResource struct {
	client *VirtuosoClient
}

type SnapshotResourceModel struct {
	ID            types.String `tfsdk:"id"`
	VM            types.String `tfsdk:"vm"`
	Name          types.String `tfsdk:"name"`
	RetentionDays types.Int64  `tfsdk:"retention_days"`
	// Computed
	SnapshotID  types.String `tfsdk:"snapshot_id"`
	Status      types.String `tfsdk:"status"`
	Source      types.String `tfsdk:"source"`
	SizeBytes   types.Int64  `tfsdk:"size_bytes"`
	Consistency types.String `tfsdk:"consistency"`
	CreatedAt   types.String `tfsdk:"created_at"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
}

func NewSnapshotResource() resource.Resource { return &SnapshotResource{} }

func (r *SnapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *SnapshotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Takes a snapshot, a full copy of a VM's disk, and deletes it when the resource is destroyed. " +
			"A snapshot of a running VM is crash consistent, as if the power had been cut. Every argument is fixed at creation, so changing one takes a new snapshot. " +
			"A snapshot with retention_days above 0 is removed by virtuOSo when it expires, after which Terraform plans to create it again; " +
			"use the default of 0 for snapshots that Terraform should keep. Restoring a snapshot is not modelled in Terraform, because a restore is an action and not a state: " +
			"use the web UI, the API, the CLI or MCP. Requires virtuOSo v0.7.0 or later.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "vm/snapshot_id.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vm": schema.StringAttribute{
				Description:   "Name of the VM to snapshot.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description:   "A label for the snapshot, up to 80 characters.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(""),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"retention_days": schema.Int64Attribute{
				Description:   "Days until virtuOSo deletes the snapshot: 1, 3, 5, 7, 14 or 28. 0 keeps it until you delete it.",
				Optional:      true,
				Computed:      true,
				Default:       int64default.StaticInt64(0),
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"snapshot_id": schema.StringAttribute{
				Description:   "The snapshot's id on the server.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status":      schema.StringAttribute{Description: "creating, ready or failed.", Computed: true},
			"source":      schema.StringAttribute{Description: "manual, scheduled or before-restore.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"size_bytes":  schema.Int64Attribute{Description: "Size of the copy on disk.", Computed: true},
			"consistency": schema.StringAttribute{Description: "clean if the VM was stopped, crash-consistent if it was running.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"created_at":  schema.StringAttribute{Description: "When the snapshot was taken (UTC).", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"expires_at":  schema.StringAttribute{Description: "When virtuOSo will delete it. Empty if it never expires.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *SnapshotResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var days types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("retention_days"), &days)...)
	if !days.IsNull() && !days.IsUnknown() {
		if d := days.ValueInt64(); d != 0 && !retentionChoices[d] {
			resp.Diagnostics.AddAttributeError(path.Root("retention_days"), "Invalid retention",
				fmt.Sprintf("retention_days must be 0, 1, 3, 5, 7, 14 or 28, got %d.", d))
		}
	}
}

func (r *SnapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func mapSnapshot(s *Snapshot, m *SnapshotResourceModel) {
	m.ID = types.StringValue(s.VM + "/" + s.ID)
	m.VM = types.StringValue(s.VM)
	m.Name = types.StringValue(s.Name)
	m.RetentionDays = types.Int64Value(s.RetentionDays)
	m.SnapshotID = types.StringValue(s.ID)
	m.Status = types.StringValue(s.Status)
	m.Source = types.StringValue(s.Source)
	m.SizeBytes = types.Int64Value(s.SizeBytes)
	m.Consistency = types.StringValue(s.Consistency)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.ExpiresAt = types.StringValue(s.ExpiresAt)
}

func (r *SnapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SnapshotResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vm := plan.VM.ValueString()
	tflog.Info(ctx, "Taking snapshot", map[string]interface{}{"vm": vm})
	snap, err := r.client.CreateSnapshot(vm, SnapshotCreateRequest{Name: plan.Name.ValueString(), RetentionDays: plan.RetentionDays.ValueInt64()})
	if err != nil {
		resp.Diagnostics.AddError("Error taking snapshot", err.Error())
		return
	}
	// Track it in state before waiting so a timeout leaves it tainted, not orphaned.
	mapSnapshot(snap, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ready, err := r.client.WaitForSnapshot(vm, snap.ID, snapshotReadyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for snapshot", err.Error())
		return
	}
	mapSnapshot(ready, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SnapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SnapshotResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	snap, err := r.client.GetSnapshot(state.VM.ValueString(), state.SnapshotID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading snapshot", err.Error())
		return
	}
	if snap == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapSnapshot(snap, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Every argument forces replacement, so Update only ever sees computed refreshes.
func (r *SnapshotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SnapshotResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	snap, err := r.client.GetSnapshot(plan.VM.ValueString(), plan.SnapshotID.ValueString())
	if err != nil || snap == nil {
		resp.Diagnostics.AddError("Error reading snapshot", "snapshot not found or unreadable")
		return
	}
	mapSnapshot(snap, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SnapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SnapshotResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Info(ctx, "Deleting snapshot", map[string]interface{}{"id": state.ID.ValueString()})
	if err := r.client.DeleteSnapshot(state.VM.ValueString(), state.SnapshotID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting snapshot", err.Error())
	}
}

// ImportState imports a snapshot by "vm/snapshot_id".
func (r *SnapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vm, id, ok := strings.Cut(req.ID, "/")
	if !ok || vm == "" || id == "" {
		resp.Diagnostics.AddError("Invalid import ID", "Use the form vm/snapshot_id, for example web1/snap-20260301-020000-ab12cd.")
		return
	}
	snap, err := r.client.GetSnapshot(vm, id)
	if err != nil {
		resp.Diagnostics.AddError("Error importing snapshot", err.Error())
		return
	}
	if snap == nil {
		resp.Diagnostics.AddError("Snapshot not found", "No snapshot "+id+" for VM "+vm)
		return
	}
	var m SnapshotResourceModel
	mapSnapshot(snap, &m)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
