package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &VMsDataSource{}

type VMsDataSource struct {
	client *VirtuosoClient
}

type VMsDataSourceModel struct {
	VMs []VMsEntryModel `tfsdk:"vms"`
}

type VMsEntryModel struct {
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
	Tags      types.Map     `tfsdk:"tags"`
	CPUPercent   types.Float64 `tfsdk:"cpu_percent"`
	MemoryUsedMB types.Int64   `tfsdk:"memory_used_mb"`
}

func NewVMsDataSource() datasource.DataSource {
	return &VMsDataSource{}
}

func (d *VMsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vms"
}

func (d *VMsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	vmAttrs := map[string]schema.Attribute{
		"name":        schema.StringAttribute{Description: "VM name.", Computed: true},
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
		"cpu_percent":    schema.Float64Attribute{Description: "Share of the VM's own vCPUs it is using now, 0 to 100, measured by the server it runs on. Null when the VM is not running or has not been measured yet.", Computed: true},
		"memory_used_mb": schema.Int64Attribute{Description: "Memory in MB the server holds for the VM now. Null when the VM is not running or has not been measured yet.", Computed: true},
		"tags":        schema.MapAttribute{Description: "Labels on the VM, as key to value. Empty when it has none.", ElementType: types.StringType, Computed: true},
	}

	resp.Schema = schema.Schema{
		Description: "Lists all virtuOSo VMs.",
		Attributes: map[string]schema.Attribute{
			"vms": schema.ListNestedAttribute{
				Description: "List of VMs.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: vmAttrs,
				},
			},
		},
	}
}

func (d *VMsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *VMsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	vms, err := d.client.ListVMs()
	if err != nil {
		resp.Diagnostics.AddError("Error listing VMs", err.Error())
		return
	}

	var model VMsDataSourceModel
	for _, vm := range vms {
		model.VMs = append(model.VMs, VMsEntryModel{
			Name:      types.StringValue(vm.Name),
			State:     types.StringValue(vm.State),
			IP:        types.StringValue(vm.IP),
			VCPUs:     types.Int64Value(vm.VCPUs),
			MemoryMB:  types.Int64Value(vm.MemoryMB),
			Autostart: types.StringValue(vm.Autostart),
			HasVNC:    types.BoolValue(vm.HasVNC),
			DiskCapGB: types.Float64Value(vm.DiskCapGB),
			DiskUsedGB: types.Float64Value(vm.DiskUsedGB),
			CloudInit: types.StringValue(vm.CloudInit),
			Network:   types.StringValue(vm.Network),
			Tags:      tagsToMap(vm.Tags),
			CPUPercent:   usageFloat(vm.CPUPercent),
			MemoryUsedMB: usageInt(vm.MemoryUsedMB),
		})
	}

	// Ensure empty list instead of null
	if model.VMs == nil {
		model.VMs = []VMsEntryModel{}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// usageFloat and usageInt turn a figure the server may have left out into null.
func usageFloat(f *float64) types.Float64 {
	if f == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*f)
}

func usageInt(i *int64) types.Int64 {
	if i == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*i)
}
