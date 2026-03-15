package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &VMDataSource{}

type VMDataSource struct {
	client *VirtuosoClient
}

type VMDataSourceModel struct {
	Name      types.String  `tfsdk:"name"`
	State     types.String  `tfsdk:"state"`
	IP        types.String  `tfsdk:"ip"`
	VCPUs     types.Int64   `tfsdk:"vcpus"`
	MemoryMB  types.Int64   `tfsdk:"memory_mb"`
	Autostart types.String  `tfsdk:"autostart"`
	HasVNC    types.Bool    `tfsdk:"has_vnc"`
	DiskCapGB types.Float64 `tfsdk:"disk_cap_gb"`
	DiskUsedGB types.Float64 `tfsdk:"disk_used_gb"`
	CloudInit types.String  `tfsdk:"cloud_init"`
	Network   types.String  `tfsdk:"network"`
	Password  types.String  `tfsdk:"password"`
	OSID      types.String  `tfsdk:"os_id"`
}

func NewVMDataSource() datasource.DataSource {
	return &VMDataSource{}
}

func (d *VMDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (d *VMDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads details of a single virtuOSo VM.",
		Attributes: map[string]schema.Attribute{
			"name":        schema.StringAttribute{Description: "VM name.", Required: true},
			"state":       schema.StringAttribute{Description: "VM state.", Computed: true},
			"ip":          schema.StringAttribute{Description: "IP address.", Computed: true},
			"vcpus":       schema.Int64Attribute{Description: "Virtual CPUs.", Computed: true},
			"memory_mb":   schema.Int64Attribute{Description: "Memory in MB.", Computed: true},
			"autostart":   schema.StringAttribute{Description: "Autostart setting.", Computed: true},
			"has_vnc":     schema.BoolAttribute{Description: "VNC available.", Computed: true},
			"disk_cap_gb": schema.Float64Attribute{Description: "Disk capacity GB.", Computed: true},
			"disk_used_gb": schema.Float64Attribute{Description: "Disk used GB.", Computed: true},
			"cloud_init":  schema.StringAttribute{Description: "Cloud-init status.", Computed: true},
			"network":     schema.StringAttribute{Description: "Network name.", Computed: true},
			"password":    schema.StringAttribute{Description: "VM password.", Computed: true, Sensitive: true},
			"os_id":       schema.StringAttribute{Description: "OS identifier.", Computed: true},
		},
	}
}

func (d *VMDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*VirtuosoClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Expected *VirtuosoClient")
		return
	}
	d.client = client
}

func (d *VMDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config VMDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vm, err := d.client.GetVM(config.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading VM", err.Error())
		return
	}
	if vm == nil {
		resp.Diagnostics.AddError("VM not found", "No VM named "+config.Name.ValueString())
		return
	}

	config.State = types.StringValue(vm.State)
	config.IP = types.StringValue(vm.IP)
	config.VCPUs = types.Int64Value(vm.VCPUs)
	config.MemoryMB = types.Int64Value(vm.MemoryMB)
	config.Autostart = types.StringValue(vm.Autostart)
	config.HasVNC = types.BoolValue(vm.HasVNC)
	config.DiskCapGB = types.Float64Value(vm.DiskCapGB)
	config.DiskUsedGB = types.Float64Value(vm.DiskUsedGB)
	config.CloudInit = types.StringValue(vm.CloudInit)
	config.Network = types.StringValue(vm.Network)
	config.Password = types.StringValue(vm.Password)
	config.OSID = types.StringValue(vm.OSID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
