package provider

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                   = &AWSInstanceResource{}
	_ resource.ResourceWithValidateConfig = &AWSInstanceResource{}
)

// The root disk sizes virtuOSo accepts for an AWS instance.
const (
	awsMinDiskGB = 8
	awsMaxDiskGB = 2000
)

const awsReadyTimeout = 15 * time.Minute

var awsInstanceIDRe = regexp.MustCompile(`^i-[0-9a-f]{8,17}$`)

type AWSInstanceResource struct {
	client *VirtuosoClient
}

type AWSPortModel struct {
	Port   types.Int64  `tfsdk:"port"`
	Source types.String `tfsdk:"source"`
}

type AWSInstanceResourceModel struct {
	ID           types.String   `tfsdk:"id"`
	Name         types.String   `tfsdk:"name"`
	InstanceType types.String   `tfsdk:"instance_type"`
	Region       types.String   `tfsdk:"region"`
	OS           types.String   `tfsdk:"os"`
	SSHKey       types.String   `tfsdk:"ssh_key"`
	UserScript   types.String   `tfsdk:"user_script"`
	DiskGB       types.Int64    `tfsdk:"disk_gb"`
	Ports        []AWSPortModel `tfsdk:"ports"`
	// Computed
	State     types.String  `tfsdk:"state"`
	PublicIP  types.String  `tfsdk:"public_ip"`
	PrivateIP types.String  `tfsdk:"private_ip"`
	IP        types.String  `tfsdk:"ip"`
	VCPUs     types.Int64   `tfsdk:"vcpus"`
	HourlyUSD types.Float64 `tfsdk:"estimated_hourly_usd"`
	Password  types.String  `tfsdk:"password"`
}

func NewAWSInstanceResource() resource.Resource { return &AWSInstanceResource{} }

func (r *AWSInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_aws_instance"
}

func (r *AWSInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages an EC2 instance launched through virtuOSo, in the AWS account saved in the server's Settings. " +
			"The AWS credentials stay on the virtuOSo server. It costs money while it runs, and running apply is the confirmation: " +
			"each instance type's price is in the server's AWS documentation. Requires an admin API key. All arguments are fixed at " +
			"creation, so changing one replaces the instance. There is no automatic termination here, because an instance that ended " +
			"by itself would not match its state; use terraform destroy. Only instances launched this way can be destroyed this way.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The EC2 instance id.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Instance name (letters, numbers, dots, hyphens and underscores). It must not already be in use.",
				Required:      true,
				PlanModifiers: replace,
			},
			"instance_type": schema.StringAttribute{
				Description:   "EC2 instance type: t3.micro, t3.small, t3.medium, t3.large, t3.xlarge, t3.2xlarge, m5.2xlarge or m5.4xlarge.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("t3.micro"),
				PlanModifiers: replace,
			},
			"region": schema.StringAttribute{
				Description: "AWS region, one the account can use. Defaults to the default region in the server's Settings.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"os": schema.StringAttribute{
				Description:   "Operating system: ubuntu-24.04 (default), ubuntu-26.04, ubuntu-22.04 or amazon-2023.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("ubuntu-24.04"),
				PlanModifiers: replace,
			},
			"ssh_key": schema.StringAttribute{
				Description:   "SSH public key to install.",
				Optional:      true,
				PlanModifiers: replace,
			},
			"user_script": schema.StringAttribute{
				Description:   "Shell script to run on first boot.",
				Optional:      true,
				PlanModifiers: replace,
			},
			"disk_gb": schema.Int64Attribute{
				Description: "Root disk size in GB, 8 to 2000. Left out, the image's own size (8 GB) is used. The disk is billed while the " +
					"instance exists, about $0.08 per GB a month, and is deleted with the instance.",
				Optional:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"ports": schema.ListNestedAttribute{
				Description:   "Extra ports to open. SSH (22) is always open. At most 10.",
				Optional:      true,
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port": schema.Int64Attribute{Description: "Port number, 1 to 65535.", Required: true},
						"source": schema.StringAttribute{
							Description: "CIDR allowed to connect, such as 203.0.113.7/32. Defaults to 0.0.0.0/0, which is anywhere.",
							Optional:    true,
							Computed:    true,
							Default:     stringdefault.StaticString("0.0.0.0/0"),
						},
					},
				},
			},
			"state": schema.StringAttribute{Description: "EC2 state: pending, running, stopping or stopped.", Computed: true},
			"public_ip": schema.StringAttribute{
				Description: "Public IP address, if it has one.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{Description: "Private IP address in the VPC.", Computed: true},
			"ip": schema.StringAttribute{
				Description: "The address to connect to: the public address, or the private one if there is no public address.",
				Computed:    true,
			},
			"vcpus": schema.Int64Attribute{Description: "Number of vCPUs.", Computed: true},
			"estimated_hourly_usd": schema.Float64Attribute{
				Description: "Estimated cost per hour while it runs, from on demand us-east-1 prices. The disk and data transfer are extra.",
				Computed:    true,
			},
			"password": schema.StringAttribute{
				Description:   "Password virtuOSo generated for the instance.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig refuses a disk size the server would refuse, at plan time and not after a launch attempt.
func (r *AWSInstanceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var disk types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("disk_gb"), &disk)...)
	if resp.Diagnostics.HasError() || disk.IsNull() || disk.IsUnknown() {
		return
	}
	if n := disk.ValueInt64(); n < awsMinDiskGB || n > awsMaxDiskGB {
		resp.Diagnostics.AddAttributeError(path.Root("disk_gb"), "Invalid disk size",
			fmt.Sprintf("disk_gb must be between %d and %d GB, or left out to use the image's own size. Got %d.", awsMinDiskGB, awsMaxDiskGB, n))
	}
}

func (r *AWSInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func mapAWSInstance(in *AWSInstance, m *AWSInstanceResourceModel) {
	m.ID = types.StringValue(in.ID)
	m.Name = types.StringValue(in.Name)
	m.Region = types.StringValue(in.Region)
	m.InstanceType = types.StringValue(in.Type)
	m.State = types.StringValue(in.State)
	m.PublicIP = types.StringValue(in.PublicIP)
	m.PrivateIP = types.StringValue(in.PrivateIP)
	m.IP = types.StringValue(in.Address())
	m.VCPUs = types.Int64Value(in.VCPUs)
	if in.HourlyUSD != nil {
		m.HourlyUSD = types.Float64Value(*in.HourlyUSD)
	} else {
		m.HourlyUSD = types.Float64Value(0)
	}
}

// launchRequestFromPlan turns what the configuration asks for into the launch request. A disk size that is not
// set is left out of the request, so the server keeps the image's own size.
func launchRequestFromPlan(plan AWSInstanceResourceModel) AWSLaunchRequest {
	launch := AWSLaunchRequest{
		Name:         plan.Name.ValueString(),
		InstanceType: plan.InstanceType.ValueString(),
		OS:           plan.OS.ValueString(),
		SSHKey:       plan.SSHKey.ValueString(),
		UserScript:   plan.UserScript.ValueString(),
		DiskGB:       plan.DiskGB.ValueInt64(),
	}
	if !plan.Region.IsNull() && !plan.Region.IsUnknown() {
		launch.Region = plan.Region.ValueString()
	}
	for _, p := range plan.Ports {
		launch.Ports = append(launch.Ports, AWSPort{Port: p.Port.ValueInt64(), Source: p.Source.ValueString()})
	}
	return launch
}

func (r *AWSInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AWSInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	launch := launchRequestFromPlan(plan)

	tflog.Info(ctx, "Launching AWS instance", map[string]interface{}{"name": launch.Name, "type": launch.InstanceType})
	out, err := r.client.LaunchAWSInstance(launch)
	if err != nil {
		resp.Diagnostics.AddError("Error launching AWS instance", err.Error())
		return
	}

	// Keep the name as the id until the instance id is known, so a timeout leaves a resource that can be
	// destroyed, not an orphaned instance that Terraform has never heard of.
	plan.ID = types.StringValue(plan.Name.ValueString())
	plan.Password = types.StringValue(out.Password)
	plan.Region = types.StringValue(out.Region)
	plan.InstanceType = types.StringValue(out.InstanceType)
	plan.State = types.StringValue("pending")
	plan.PublicIP, plan.PrivateIP, plan.IP = types.StringValue(""), types.StringValue(""), types.StringValue("")
	plan.VCPUs = types.Int64Value(0)
	plan.HourlyUSD = types.Float64Value(out.HourlyUSD)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, err := r.client.WaitForAWSInstance(launch.Name, out.Region, awsReadyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for the AWS instance", err.Error()+". It may still be starting: check with vm fleet aws on the virtuOSo server.")
		return
	}
	mapAWSInstance(in, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AWSInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AWSInstanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := r.lookup(&state)
	if err != nil {
		resp.Diagnostics.AddError("Error reading AWS instance", err.Error())
		return
	}
	if in == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapAWSInstance(in, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// lookup finds the instance by id, or by name when the id is still the placeholder left by an interrupted create.
func (r *AWSInstanceResource) lookup(m *AWSInstanceResourceModel) (*AWSInstance, error) {
	id := m.ID.ValueString()
	if awsInstanceIDRe.MatchString(id) {
		return r.client.GetAWSInstance(id)
	}
	return r.client.FindAWSInstance(m.Name.ValueString(), m.Region.ValueString())
}

// Every argument forces replacement, so Update only ever sees computed refreshes.
func (r *AWSInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AWSInstanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in, err := r.lookup(&plan)
	if err != nil || in == nil {
		resp.Diagnostics.AddError("Error reading AWS instance", "the instance was not found or could not be read")
		return
	}
	mapAWSInstance(in, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AWSInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AWSInstanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Info(ctx, "Terminating AWS instance", map[string]interface{}{"id": state.ID.ValueString(), "name": state.Name.ValueString()})
	if err := r.terminate(&state); err != nil {
		resp.Diagnostics.AddError("Error terminating AWS instance", err.Error())
	}
}

// terminate ends the instance in the model. When the id is still the placeholder left by an interrupted create,
// the instance id was never recorded, so it is found by name; if it never appeared there is nothing to end.
func (r *AWSInstanceResource) terminate(m *AWSInstanceResourceModel) error {
	id := m.ID.ValueString()
	if !awsInstanceIDRe.MatchString(id) {
		in, err := r.client.FindAWSInstance(m.Name.ValueString(), m.Region.ValueString())
		if err != nil {
			return fmt.Errorf("finding the instance to terminate: %w", err)
		}
		if in == nil {
			return nil
		}
		id = in.ID
	}
	if err := r.client.TerminateAWSInstance(id); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	return nil
}
