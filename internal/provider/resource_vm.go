package provider

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &VMResource{}
	_ resource.ResourceWithImportState    = &VMResource{}
	_ resource.ResourceWithValidateConfig = &VMResource{}
)

type VMResource struct {
	client *VirtuosoClient
}

type VMResourceModel struct {
	Name                types.String `tfsdk:"name"`
	Size                types.String `tfsdk:"size"`
	OS                  types.String `tfsdk:"os"`
	SSHKey              types.String `tfsdk:"ssh_key"`
	DiskGB              types.Int64  `tfsdk:"disk_gb"`
	Password            types.String `tfsdk:"password"`
	VNC                 types.Bool   `tfsdk:"vnc"`
	Desktop             types.Bool   `tfsdk:"desktop"`
	UserScript          types.String `tfsdk:"user_script"`
	PostTailscaleScript types.String `tfsdk:"post_tailscale_script"`
	Tailscale           types.Bool   `tfsdk:"tailscale"`
	ISO                 types.String `tfsdk:"iso"`
	Bridged             types.Bool   `tfsdk:"bridged"`
	Autostart           types.Bool   `tfsdk:"autostart"`
	Protected           types.Bool   `tfsdk:"protected"`
	Notes               types.String `tfsdk:"notes"`
	Tags                types.Map    `tfsdk:"tags"`
	Node                types.String `tfsdk:"node"`
	VPC                 types.String `tfsdk:"vpc"`
	SecurityGroups      types.Set    `tfsdk:"security_groups"`
	VPCIP               types.String `tfsdk:"vpc_ip"`
	Started             types.Bool   `tfsdk:"started"`
	// Computed
	IP         types.String  `tfsdk:"ip"`
	State      types.String  `tfsdk:"state"`
	VCPUs      types.Int64   `tfsdk:"vcpus"`
	MemoryMB   types.Int64   `tfsdk:"memory_mb"`
	DiskCapGB  types.Float64 `tfsdk:"disk_cap_gb"`
	DiskUsedGB types.Float64 `tfsdk:"disk_used_gb"`
	HasVNC     types.Bool    `tfsdk:"has_vnc"`
	CloudInit  types.String  `tfsdk:"cloud_init"`
	Network    types.String  `tfsdk:"network"`
	OSID       types.String  `tfsdk:"os_id"`
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
			"post_tailscale_script": schema.StringAttribute{
				Description: "Bash script that runs once after Tailscale connects, or at first boot once the network is up when the VM is not on Tailscale (tailscale = false or no server key). It sees TS_IP and TS_DNSNAME, survives a reboot during first boot, and is retried at the next boot if it fails. Requires a virtuOSo server that supports post_tailscale_script.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tailscale": tailscaleAttribute("VM"),
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
			"notes": schema.StringAttribute{
				Description: "Free text (Markdown) notes for the VM, up to 64 KB. When omitted, notes already on the VM are kept. Set to an empty string to clear them.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Labels for the VM, as key to value (an empty value is fine). Up to 20 tags. A key uses letters, digits and - _ . : and starts with a letter or digit, up to 40 characters; a value is up to 100. Keys that start with virtuoso: are reserved. When set, the VM has exactly these tags and any other tag on it is removed. When omitted, tags already on the VM are kept. Needs a virtuOSo server that supports tags.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"node": schema.StringAttribute{
				Description: "Name of a fleet node to run the VM on, for a provider pointed at the fleet hub. The hub starts the VM on that node, reads it from the fleet list and deletes it (stopping it first if it is running). Works with vpc, so a VPC made on the hub can have VMs on any node. A VM on a node cannot use iso, and disk_gb and bridged cannot change in place (replace the VM). Everything else works as for any VM: tags, notes, protected, autostart, started and security_groups. Changing node replaces the VM. Needs a hub with fleet support.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc": schema.StringAttribute{
				Description: "Name of a VPC to launch the VM into. The VM then has one network card, on the VPC, and gets a private address from it (see vpc_ip). A VM joins a VPC only when it is launched, so changing this replaces the VM. Cannot be combined with bridged. Needs a virtuOSo server that supports VPCs.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"security_groups": schema.SetAttribute{
				Description: "Names of the VPC's security groups the VM's card is in, up to 5. Only with vpc. When left out, the VM is in the group default. Changing the set updates the VM in place.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_ip": schema.StringAttribute{
				Description: "The VM's private address in its VPC.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
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
	r = r.withNode(plan.Node)

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
	if !plan.PostTailscaleScript.IsNull() {
		launchReq.PostTailscaleScript = plan.PostTailscaleScript.ValueString()
	}
	launchReq.Tailscale = tailscaleOptOut(plan.Tailscale)
	if !plan.ISO.IsNull() {
		launchReq.ISO = plan.ISO.ValueString()
	}
	if !plan.Bridged.IsNull() {
		launchReq.Bridged = plan.Bridged.ValueBool()
	}

	wantTags, tagDiags := planTags(ctx, plan.Tags)
	resp.Diagnostics.Append(tagDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(wantTags) > 0 {
		launchReq.Tags = wantTags
	}
	if !plan.VPC.IsNull() && !plan.VPC.IsUnknown() {
		launchReq.VPC = plan.VPC.ValueString()
		groups, gd := planGroups(ctx, plan.SecurityGroups)
		resp.Diagnostics.Append(gd...)
		if resp.Diagnostics.HasError() {
			return
		}
		launchReq.SecurityGroups = groups
	}

	tflog.Info(ctx, "Launching VM", map[string]interface{}{"name": launchReq.Name, "node": plan.Node.ValueString()})

	var launchResp *LaunchResponse
	err := retryVPCNotFound(6, vpcRetryWait, func() error {
		var lerr error
		if hasNode(plan.Node) {
			// Through the hub, onto the chosen node.
			var pw string
			pw, lerr = r.client.FleetLaunch(FleetLaunchRequest{LaunchRequest: launchReq, Node: plan.Node.ValueString()})
			launchResp = &LaunchResponse{Name: launchReq.Name, Password: pw}
			return lerr
		}
		launchResp, lerr = r.client.LaunchVM(launchReq)
		return lerr
	})
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

	// Handle notes
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() && plan.Notes.ValueString() != "" {
		if err := r.client.SetNotes(launchReq.Name, plan.Notes.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error setting notes", err.Error())
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
	resp.Diagnostics.Append(r.syncVPC(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r = r.withNode(state.Node)
	vm, err := r.client.GetVM(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM", err.Error())
		return
	}
	if vm == nil && hasNode(state.Node) {
		// The node no longer has it. A Fleet move made outside Terraform leaves it on another node, and
		// dropping it from the state would make the next plan create a second VM of the same name.
		if moved := r.movedTo(state); moved != "" {
			r2 := r.withNode(types.StringValue(moved))
			if v2, err2 := r2.client.GetVM(state.Name.ValueString()); err2 == nil && v2 != nil {
				resp.Diagnostics.AddWarning("VM "+state.Name.ValueString()+" is now on node "+moved,
					"It was on "+state.Node.ValueString()+" and was moved outside Terraform. The state now follows it. Set node = \""+moved+"\" in the configuration, or the next plan replaces the VM to put it back on "+state.Node.ValueString()+".")
				state.Node = types.StringValue(moved)
				r, vm = r2, v2
			}
		}
	}
	if vm == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	r.mapVMToState(vm, &state)
	resp.Diagnostics.Append(r.syncVPC(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
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
	r = r.withNode(plan.Node)
	if hasNode(plan.Node) {
		// The hub forwards everything below except these two, which have no hub call.
		if plan.Bridged.ValueBool() != state.Bridged.ValueBool() || (!plan.DiskGB.IsNull() && !plan.DiskGB.IsUnknown() && plan.DiskGB.ValueInt64() != state.DiskGB.ValueInt64()) {
			resp.Diagnostics.AddError("Cannot change bridged or disk_gb on a VM that runs on a node",
				"The hub cannot change the network mode or the disk size of a VM on another node. Replace the VM (terraform apply -replace) or change it on the node itself.")
			return
		}
	}

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

	// Handle notes change
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() && plan.Notes.ValueString() != state.Notes.ValueString() {
		tflog.Info(ctx, "Updating notes", map[string]interface{}{"name": name})
		if err := r.client.SetNotes(name, plan.Notes.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error setting notes", err.Error())
			return
		}
	}

	// Handle tags change. The server's own list is the starting point, so a tag added outside Terraform
	// is removed when tags are set here.
	if want, tagDiags := planTags(ctx, plan.Tags); tagDiags.HasError() {
		resp.Diagnostics.Append(tagDiags...)
		return
	} else if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() && !plan.Tags.Equal(state.Tags) {
		tflog.Info(ctx, "Updating tags", map[string]interface{}{"name": name})
		current, err := r.client.GetVM(name)
		if err != nil || current == nil {
			resp.Diagnostics.AddError("Error reading VM tags", errOrNotFound(err, name))
			return
		}
		if err := r.client.ReplaceTags(name, current.Tags, want); err != nil {
			resp.Diagnostics.AddError("Error setting tags", err.Error())
			return
		}
	}

	// Handle security group change. The VM stays where it is; only the groups of its card change.
	if !plan.VPC.IsNull() && !plan.SecurityGroups.IsNull() && !plan.SecurityGroups.IsUnknown() && !plan.SecurityGroups.Equal(state.SecurityGroups) {
		groups, gd := planGroups(ctx, plan.SecurityGroups)
		resp.Diagnostics.Append(gd...)
		if resp.Diagnostics.HasError() {
			return
		}
		tflog.Info(ctx, "Updating security groups", map[string]interface{}{"name": name})
		if _, err := r.client.SetVMSecurityGroups(plan.VPC.ValueString(), name, groups); err != nil {
			resp.Diagnostics.AddError("Error setting security groups", err.Error())
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
	resp.Diagnostics.Append(r.syncVPC(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *VMResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VMResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Deleting VM", map[string]interface{}{"name": state.Name.ValueString()})
	r = r.withNode(state.Node)

	// Auto-unprotect before destroying
	if state.Protected.ValueBool() {
		tflog.Info(ctx, "Removing protection before destroy", map[string]interface{}{"name": state.Name.ValueString()})
		if err := r.client.SetProtected(state.Name.ValueString(), false); err != nil {
			resp.Diagnostics.AddError("Error removing protection", err.Error())
			return
		}
	}

	if hasNode(state.Node) {
		// The hub shuts a running VM down and deletes it.
		if _, err := r.client.DeleteFleetVM(state.Node.ValueString(), state.Name.ValueString(), true); err != nil {
			resp.Diagnostics.AddError("Error deleting VM on node "+state.Node.ValueString(), err.Error())
		}
		return
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
	state.PostTailscaleScript = types.StringNull()
	state.Tailscale = types.BoolNull() // not known after import; the configuration value is kept without a replacement
	state.ISO = types.StringNull()
	state.Tags = types.MapNull(types.StringType)
	state.VPC = types.StringNull()
	if name, err := r.client.FindVPCOfVM(vm.Name); err != nil {
		resp.Diagnostics.AddWarning("Could not look for the VM's VPC", err.Error())
	} else if name != "" {
		state.VPC = types.StringValue(name)
	}
	r.mapVMToState(vm, &state)
	resp.Diagnostics.Append(r.syncVPC(ctx, &state)...)

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

	// Notes from API
	state.Notes = types.StringValue(vm.Notes)

	// Tags from API. A VM with none has an empty map, not null, so an omitted attribute stays quiet.
	state.Tags = tagsToMap(vm.Tags)

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

// tagsToMap turns a VM's tags into a Terraform map. No tags is an empty map.
func tagsToMap(tags map[string]string) types.Map {
	elems := make(map[string]attr.Value, len(tags))
	for k, v := range tags {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

// planTags reads the tags a plan asks for. A null or unknown map is no request, and gives no tags.
func planTags(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	out := map[string]string{}
	d := m.ElementsAs(ctx, &out, false)
	return out, d
}

func errOrNotFound(err error, name string) string {
	if err != nil {
		return err.Error()
	}
	return "No VM named " + name
}

// planGroups reads the security group names from the plan. Nothing when the set is left out.
func planGroups(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var out []string
	d := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, d
}

// syncVPC fills vpc_ip and security_groups from the server. A VM that is not in the VPC the state names has
// left it, so vpc is cleared and the next plan shows the difference. Without a VPC both are null.
func (r *VMResource) syncVPC(ctx context.Context, state *VMResourceModel) diag.Diagnostics {
	state.VPCIP = types.StringNull()
	state.SecurityGroups = types.SetNull(types.StringType)
	if state.VPC.IsNull() || state.VPC.IsUnknown() {
		state.VPC = types.StringNull()
		return nil
	}
	name := state.Name.ValueString()
	v, err := r.client.GetVPC(state.VPC.ValueString())
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Error reading the VM's VPC", err.Error())
		return d
	}
	var ip string
	if v != nil {
		for _, c := range v.VMs {
			if c.VM == name {
				ip = c.IP
			}
		}
	}
	if ip == "" {
		state.VPC = types.StringNull()
		return nil
	}
	state.VPCIP = types.StringValue(ip)
	groups, found, err := r.client.VMSecurityGroups(state.VPC.ValueString(), name)
	if err != nil {
		var d diag.Diagnostics
		d.AddError("Error reading the VM's security groups", err.Error())
		return d
	}
	if !found {
		return nil
	}
	set, d := types.SetValueFrom(ctx, types.StringType, groups)
	state.SecurityGroups = set
	return d
}

// ValidateConfig catches combinations the server would refuse, before anything is launched.
func (r *VMResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c VMResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if hasNode(c.Node) && !c.ISO.IsNull() {
		resp.Diagnostics.AddError("iso is not available with node", "An ISO boot runs only on the server that holds the file. Leave node out and use that server's own endpoint.")
	}
	if c.VPC.IsUnknown() {
		return
	}
	if c.VPC.IsNull() && !c.SecurityGroups.IsNull() && !c.SecurityGroups.IsUnknown() {
		resp.Diagnostics.AddError("security_groups needs vpc", "Security groups belong to a VPC. Set vpc, or leave security_groups out.")
	}
	if !c.VPC.IsNull() && !c.Bridged.IsNull() && !c.Bridged.IsUnknown() && c.Bridged.ValueBool() {
		resp.Diagnostics.AddError("vpc cannot be combined with bridged", "A VM in a VPC has one network card, on the VPC.")
	}
	if !c.SecurityGroups.IsNull() && !c.SecurityGroups.IsUnknown() && len(c.SecurityGroups.Elements()) > 5 {
		resp.Diagnostics.AddError("Too many security groups", "A VM's card can be in at most 5 groups.")
	}
}
