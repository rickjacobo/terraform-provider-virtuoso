package provider

import (
	"context"
	"strings"
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
	_ resource.Resource                = &VMResource{}
	_ resource.ResourceWithImportState = &VMResource{}
)

type VMResource struct {
	client *VirtuosoClient
}

type VMResourceModel struct {
	Name      types.String  `tfsdk:"name"`
	Size      types.String  `tfsdk:"size"`
	OS        types.String  `tfsdk:"os"`
	SSHKey    types.String  `tfsdk:"ssh_key"`
	DiskGB    types.Int64   `tfsdk:"disk_gb"`
	Password  types.String  `tfsdk:"password"`
	VNC       types.Bool    `tfsdk:"vnc"`
	Desktop   types.Bool    `tfsdk:"desktop"`
	UserScript types.String `tfsdk:"user_script"`
	ISO        types.String `tfsdk:"iso"`
	Bridged    types.Bool   `tfsdk:"bridged"`
	Autostart  types.Bool   `tfsdk:"autostart"`
	Protected  types.Bool   `tfsdk:"protected"`
	Started    types.Bool   `tfsdk:"started"`
	// Computed
	IP        types.String  `tfsdk:"ip"`
	State     types.String  `tfsdk:"state"`
	VCPUs     types.Int64   `tfsdk:"vcpus"`
	MemoryMB  types.Int64   `tfsdk:"memory_mb"`
	DiskCapGB types.Float64 `tfsdk:"disk_cap_gb"`
	DiskUsedGB types.Float64 `tfsdk:"disk_used_gb"`
	HasVNC    types.Bool    `tfsdk:"has_vnc"`
	CloudInit types.String  `tfsdk:"cloud_init"`
	Network   types.String  `tfsdk:"network"`
	OSID      types.String  `tfsdk:"os_id"`
}

func NewVMResource() resource.Resource {
	return &VMResource{}
}

func (r *VMResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *VMResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a virtuOSo virtual machine.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "VM name (1-63 chars, alphanumeric, hyphens, underscores, dots).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"size": schema.StringAttribute{
				Description: "VM size: micro, small, medium, large, xlarge, 2xlarge, 3xlarge, or 4xlarge.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("small"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"os": schema.StringAttribute{
				Description: "Operating system ID (e.g. ubuntu-24.04). Ignored when iso is set.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("ubuntu-24.04"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ssh_key": schema.StringAttribute{
				Description: "SSH public key to inject into the VM.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"disk_gb": schema.Int64Attribute{
				Description: "Disk size in GB. Can be increased in-place; cannot be shrunk.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"password": schema.StringAttribute{
				Description: "VM password. Server-generated if omitted.",
				Optional:    true,
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vnc": schema.BoolAttribute{
				Description: "Enable VNC graphics.",
				Optional:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"desktop": schema.BoolAttribute{
				Description: "Install desktop environment (implies VNC).",
				Optional:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"user_script": schema.StringAttribute{
				Description: "Cloud-init user script to run on first boot.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"iso": schema.StringAttribute{
				Description: "Path to ISO file on the server to boot from. When set, the VM boots from the ISO instead of a cloud image (os is ignored).",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"bridged": schema.BoolAttribute{
				Description: "Use bridged networking (VM gets a LAN IP from router DHCP) instead of NAT. Requires a host bridge to be configured. Can be changed in-place (VM must be stopped, restarted automatically).",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"autostart": schema.BoolAttribute{
				Description: "Automatically start VM on host boot.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"protected": schema.BoolAttribute{
				Description: "Delete protection. When enabled, the VM cannot be deleted until protection is disabled. Terraform auto-unprotects before destroy.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"started": schema.BoolAttribute{
				Description: "Whether the VM should be running.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			// Computed attributes
			"ip": schema.StringAttribute{
				Description: "VM IP address.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"state": schema.StringAttribute{
				Description: "VM state (running, shutoff, etc.).",
				Computed:    true,
			},
			"vcpus": schema.Int64Attribute{
				Description: "Number of virtual CPUs.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"memory_mb": schema.Int64Attribute{
				Description: "Memory in megabytes.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"disk_cap_gb": schema.Float64Attribute{
				Description: "Disk capacity in GB.",
				Computed:    true,
			},
			"disk_used_gb": schema.Float64Attribute{
				Description: "Disk usage in GB.",
				Computed:    true,
			},
			"has_vnc": schema.BoolAttribute{
				Description: "Whether VNC is available.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"cloud_init": schema.StringAttribute{
				Description: "Cloud-init status.",
				Computed:    true,
			},
			"network": schema.StringAttribute{
				Description: "Libvirt network name.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"os_id": schema.StringAttribute{
				Description: "OS identifier from server.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *VMResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VMResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	launchReq := LaunchRequest{
		Name: plan.Name.ValueString(),
		OS:   plan.OS.ValueString(),
	}

	if !plan.Size.IsNull() && !plan.Size.IsUnknown() {
		launchReq.Size = plan.Size.ValueString()
	}
	if !plan.SSHKey.IsNull() {
		launchReq.SSHKey = plan.SSHKey.ValueString()
	}
	if !plan.DiskGB.IsNull() && !plan.DiskGB.IsUnknown() {
		launchReq.DiskGB = plan.DiskGB.ValueInt64()
	}
	if !plan.Password.IsNull() && !plan.Password.IsUnknown() {
		launchReq.Password = plan.Password.ValueString()
	}
	if !plan.VNC.IsNull() {
		launchReq.VNC = plan.VNC.ValueBool()
	}
	if !plan.Desktop.IsNull() {
		launchReq.Desktop = plan.Desktop.ValueBool()
	}
	if !plan.UserScript.IsNull() {
		launchReq.UserScript = plan.UserScript.ValueString()
	}
	if !plan.ISO.IsNull() {
		launchReq.ISO = plan.ISO.ValueString()
	}
	if !plan.Bridged.IsNull() {
		launchReq.Bridged = plan.Bridged.ValueBool()
	}

	tflog.Info(ctx, "Launching VM", map[string]interface{}{"name": launchReq.Name})

	launchResp, err := r.client.LaunchVM(launchReq)
	if err != nil {
		resp.Diagnostics.AddError("Error launching VM", err.Error())
		return
	}

	// Capture password from launch response
	plan.Password = types.StringValue(launchResp.Password)

	tflog.Info(ctx, "Waiting for VM to be ready", map[string]interface{}{"name": launchReq.Name})

	vm, err := r.client.WaitForVM(launchReq.Name, 10*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for VM", err.Error())
		return
	}

	// Wait for IP assignment (DHCP lease) if VM is staying running
	// Skip when booting from ISO — installer takes a long time, no IP expected
	if plan.Started.ValueBool() && plan.ISO.IsNull() {
		tflog.Info(ctx, "Waiting for VM IP address", map[string]interface{}{"name": launchReq.Name})
		vm, err = r.client.WaitForVMIP(launchReq.Name, 3*time.Minute)
		if err != nil {
			resp.Diagnostics.AddWarning("VM running but no IP yet", err.Error())
		}
	}

	// Handle started=false: stop the VM after creation
	if plan.Started.ValueBool() == false {
		tflog.Info(ctx, "Stopping VM per started=false", map[string]interface{}{"name": launchReq.Name})
		if err := r.client.StopVM(launchReq.Name); err != nil {
			resp.Diagnostics.AddError("Error stopping VM", err.Error())
			return
		}
		// Wait for it to shut off
		vm, err = r.client.WaitForVM(launchReq.Name, 2*time.Minute)
		if err != nil {
			resp.Diagnostics.AddError("Error waiting for VM to stop", err.Error())
			return
		}
	}

	// Handle autostart
	if plan.Autostart.ValueBool() {
		if err := r.client.SetAutostart(launchReq.Name, true); err != nil {
			resp.Diagnostics.AddError("Error setting autostart", err.Error())
			return
		}
	}

	// Handle protected
	if plan.Protected.ValueBool() {
		if err := r.client.SetProtected(launchReq.Name, true); err != nil {
			resp.Diagnostics.AddError("Error setting protection", err.Error())
			return
		}
	}

	// Final read to populate all computed fields
	vm, err = r.client.GetVM(launchReq.Name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM after creation", err.Error())
		return
	}

	r.mapVMToState(vm, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vm, err := r.client.GetVM(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM", err.Error())
		return
	}
	if vm == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	r.mapVMToState(vm, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VMResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state VMResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()

	// Handle autostart change
	if plan.Autostart.ValueBool() != state.Autostart.ValueBool() {
		tflog.Info(ctx, "Updating autostart", map[string]interface{}{"name": name, "enabled": plan.Autostart.ValueBool()})
		if err := r.client.SetAutostart(name, plan.Autostart.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error setting autostart", err.Error())
			return
		}
	}

	// Handle protected change
	if plan.Protected.ValueBool() != state.Protected.ValueBool() {
		tflog.Info(ctx, "Updating protection", map[string]interface{}{"name": name, "protected": plan.Protected.ValueBool()})
		if err := r.client.SetProtected(name, plan.Protected.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error setting protection", err.Error())
			return
		}
	}

	// Handle bridged change (network mode switch)
	planBridged := plan.Bridged.ValueBool()
	stateBridged := state.Bridged.ValueBool()
	if planBridged != stateBridged {
		var newNetwork string
		if planBridged {
			newNetwork = "bridge:br0"
		} else {
			newNetwork = "default"
		}
		tflog.Info(ctx, "Switching network mode", map[string]interface{}{"name": name, "bridged": planBridged, "network": newNetwork})

		// VM must be shut off — stop if running
		vm, err := r.client.GetVM(name)
		if err != nil {
			resp.Diagnostics.AddError("Error reading VM for network change", err.Error())
			return
		}
		wasRunning := vm.State == "running"

		if wasRunning {
			tflog.Info(ctx, "Stopping VM for network change", map[string]interface{}{"name": name})
			if err := r.client.StopVM(name); err != nil {
				resp.Diagnostics.AddError("Error stopping VM for network change", err.Error())
				return
			}
			if _, err := r.client.WaitForVM(name, 2*time.Minute); err != nil {
				resp.Diagnostics.AddError("Error waiting for VM to stop", err.Error())
				return
			}
		}

		if err := r.client.ChangeNetwork(name, newNetwork); err != nil {
			resp.Diagnostics.AddError("Error changing network", err.Error())
			return
		}

		if wasRunning && plan.Started.ValueBool() {
			tflog.Info(ctx, "Starting VM after network change", map[string]interface{}{"name": name})
			if err := r.client.StartVM(name); err != nil {
				resp.Diagnostics.AddError("Error starting VM after network change", err.Error())
				return
			}
			if _, err := r.client.WaitForVM(name, 2*time.Minute); err != nil {
				resp.Diagnostics.AddError("Error waiting for VM to start", err.Error())
				return
			}
		}
	}

	// Handle disk_gb change
	if !plan.DiskGB.IsNull() && !plan.DiskGB.IsUnknown() && !state.DiskGB.IsNull() && !state.DiskGB.IsUnknown() {
		planDisk := plan.DiskGB.ValueInt64()
		stateDisk := state.DiskGB.ValueInt64()
		if planDisk != stateDisk {
			if planDisk < stateDisk {
				resp.Diagnostics.AddError("Cannot shrink disk", "Disk size can only be increased, not decreased.")
				return
			}

			tflog.Info(ctx, "Resizing disk", map[string]interface{}{"name": name, "size_gb": planDisk})

			// Check if VM is running — need to stop first
			vm, err := r.client.GetVM(name)
			if err != nil {
				resp.Diagnostics.AddError("Error reading VM for resize", err.Error())
				return
			}
			wasRunning := vm.State == "running"

			if wasRunning {
				tflog.Info(ctx, "Stopping VM for disk resize", map[string]interface{}{"name": name})
				if err := r.client.StopVM(name); err != nil {
					resp.Diagnostics.AddError("Error stopping VM for resize", err.Error())
					return
				}
				if _, err := r.client.WaitForVM(name, 2*time.Minute); err != nil {
					resp.Diagnostics.AddError("Error waiting for VM to stop", err.Error())
					return
				}
			}

			if err := r.client.ResizeVM(name, planDisk); err != nil {
				resp.Diagnostics.AddError("Error resizing disk", err.Error())
				return
			}

			// Restart if it was running and user wants it started
			if wasRunning && plan.Started.ValueBool() {
				tflog.Info(ctx, "Starting VM after disk resize", map[string]interface{}{"name": name})
				if err := r.client.StartVM(name); err != nil {
					resp.Diagnostics.AddError("Error starting VM after resize", err.Error())
					return
				}
				if _, err := r.client.WaitForVM(name, 2*time.Minute); err != nil {
					resp.Diagnostics.AddError("Error waiting for VM to start", err.Error())
					return
				}
			}
		}
	}

	// Handle started change
	if plan.Started.ValueBool() != state.Started.ValueBool() {
		if plan.Started.ValueBool() {
			tflog.Info(ctx, "Starting VM", map[string]interface{}{"name": name})
			if err := r.client.StartVM(name); err != nil {
				resp.Diagnostics.AddError("Error starting VM", err.Error())
				return
			}
		} else {
			tflog.Info(ctx, "Stopping VM", map[string]interface{}{"name": name})
			if err := r.client.StopVM(name); err != nil {
				resp.Diagnostics.AddError("Error stopping VM", err.Error())
				return
			}
		}
		if _, err := r.client.WaitForVM(name, 2*time.Minute); err != nil {
			resp.Diagnostics.AddError("Error waiting for VM state change", err.Error())
			return
		}
	}

	// Final read
	vm, err := r.client.GetVM(name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM after update", err.Error())
		return
	}

	r.mapVMToState(vm, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Deleting VM", map[string]interface{}{"name": state.Name.ValueString()})

	// Auto-unprotect before destroying
	if state.Protected.ValueBool() {
		tflog.Info(ctx, "Removing protection before destroy", map[string]interface{}{"name": state.Name.ValueString()})
		if err := r.client.SetProtected(state.Name.ValueString(), false); err != nil {
			resp.Diagnostics.AddError("Error removing protection", err.Error())
			return
		}
	}

	if err := r.client.DeleteVM(state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting VM", err.Error())
	}
}

func (r *VMResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vm, err := r.client.GetVM(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing VM", err.Error())
		return
	}
	if vm == nil {
		resp.Diagnostics.AddError("VM not found", "No VM named "+req.ID)
		return
	}

	var state VMResourceModel
	state.Name = types.StringValue(vm.Name)
	// Config-only fields are null after import — user fills in or uses ignore_changes
	state.Size = types.StringNull()
	state.OS = types.StringNull()
	state.SSHKey = types.StringNull()
	state.VNC = types.BoolNull()
	state.Desktop = types.BoolNull()
	state.UserScript = types.StringNull()
	state.ISO = types.StringNull()
	r.mapVMToState(vm, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *VMResource) mapVMToState(vm *VMResponse, state *VMResourceModel) {
	state.IP = types.StringValue(vm.IP)
	state.State = types.StringValue(vm.State)
	state.VCPUs = types.Int64Value(vm.VCPUs)
	state.MemoryMB = types.Int64Value(vm.MemoryMB)
	state.DiskCapGB = types.Float64Value(vm.DiskCapGB)
	state.DiskUsedGB = types.Float64Value(vm.DiskUsedGB)
	state.HasVNC = types.BoolValue(vm.HasVNC)
	state.CloudInit = types.StringValue(vm.CloudInit)
	state.Network = types.StringValue(vm.Network)
	state.OSID = types.StringValue(vm.OSID)

	// Normalize autostart: "yes" → true, else false
	state.Autostart = types.BoolValue(vm.Autostart == "enable")

	// Protected from API
	state.Protected = types.BoolValue(vm.Protected)

	// Derive started from state
	state.Started = types.BoolValue(vm.State == "running")

	// Derive bridged from network field ("bridge:..." means bridged)
	state.Bridged = types.BoolValue(strings.HasPrefix(vm.Network, "bridge:"))

	// Set password from GET response if available and not already set
	if vm.Password != "" {
		state.Password = types.StringValue(vm.Password)
	}

	// Set disk_gb from the capacity if not already set by user
	if state.DiskGB.IsNull() || state.DiskGB.IsUnknown() {
		state.DiskGB = types.Int64Value(int64(vm.DiskCapGB))
	}
}
