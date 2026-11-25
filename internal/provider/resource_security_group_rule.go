package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &SecurityGroupRuleResource{}
	_ resource.ResourceWithImportState    = &SecurityGroupRuleResource{}
	_ resource.ResourceWithValidateConfig = &SecurityGroupRuleResource{}
)

type SecurityGroupRuleResource struct {
	client *VirtuosoClient
}

type SecurityGroupRuleResourceModel struct {
	ID            types.String `tfsdk:"id"`
	VPC           types.String `tfsdk:"vpc"`
	SecurityGroup types.String `tfsdk:"security_group"`
	Direction     types.String `tfsdk:"direction"`
	Protocol      types.String `tfsdk:"protocol"`
	PortFrom      types.Int64  `tfsdk:"port_from"`
	PortTo        types.Int64  `tfsdk:"port_to"`
	CIDR          types.String `tfsdk:"cidr"`
	SourceGroup   types.String `tfsdk:"source_group"`
	Description   types.String `tfsdk:"description"`
	RuleID        types.Int64  `tfsdk:"rule_id"`
}

func NewSecurityGroupRuleResource() resource.Resource { return &SecurityGroupRuleResource{} }

func (r *SecurityGroupRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rule"
}

func (r *SecurityGroupRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "Manages one allow rule of a security group. Rules only allow traffic; everything not allowed is dropped, and a reply to allowed traffic is allowed automatically. A rule cannot be changed in place, so any change replaces it. Give exactly one of cidr or source_group.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The VPC, group and rule number, as vpc/group/number.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc": schema.StringAttribute{
				Description: "Name of the VPC.", Required: true, PlanModifiers: replaceStr,
			},
			"security_group": schema.StringAttribute{
				Description: "Name of the security group the rule is added to.", Required: true, PlanModifiers: replaceStr,
			},
			"direction": schema.StringAttribute{
				Description: "ingress (who may send to the VM) or egress (where the VM may send).", Required: true, PlanModifiers: replaceStr,
			},
			"protocol": schema.StringAttribute{
				Description:   "all, tcp, udp or icmp. Defaults to all.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("all"),
				PlanModifiers: replaceStr,
			},
			"port_from": schema.Int64Attribute{
				Description: "First port, for tcp and udp. With neither port given, the rule covers 1 to 65535.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			"port_to": schema.Int64Attribute{
				Description: "Last port, for tcp and udp. Defaults to port_from.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			"cidr": schema.StringAttribute{
				Description: "The other side as an IPv4 range with no host bits set, for example 192.0.2.0/24 or 0.0.0.0/0.", Optional: true, PlanModifiers: replaceStr,
			},
			"source_group": schema.StringAttribute{
				Description: "The other side as another security group of the same VPC.", Optional: true, PlanModifiers: replaceStr,
			},
			"description": schema.StringAttribute{
				Description:   "Up to 120 characters.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(""),
				PlanModifiers: replaceStr,
			},
			"rule_id": schema.Int64Attribute{
				Description:   "The rule's number on the server.",
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *SecurityGroupRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var c SecurityGroupRuleResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &c)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !c.Direction.IsUnknown() && !c.Direction.IsNull() {
		if d := c.Direction.ValueString(); d != "ingress" && d != "egress" {
			resp.Diagnostics.AddAttributeError(pathRoot("direction"), "Invalid direction", "direction must be ingress or egress.")
		}
	}
	proto := "all"
	if !c.Protocol.IsUnknown() && !c.Protocol.IsNull() {
		proto = c.Protocol.ValueString()
		switch proto {
		case "all", "tcp", "udp", "icmp":
		default:
			resp.Diagnostics.AddAttributeError(pathRoot("protocol"), "Invalid protocol", "protocol must be all, tcp, udp or icmp.")
		}
	}
	if (proto == "all" || proto == "icmp") && ((!c.PortFrom.IsNull() && !c.PortFrom.IsUnknown()) || (!c.PortTo.IsNull() && !c.PortTo.IsUnknown())) {
		resp.Diagnostics.AddError("Ports need tcp or udp", "port_from and port_to only apply when protocol is tcp or udp.")
	}
	haveCIDR := !c.CIDR.IsNull() || c.CIDR.IsUnknown()
	haveGroup := !c.SourceGroup.IsNull() || c.SourceGroup.IsUnknown()
	if !c.CIDR.IsUnknown() && !c.SourceGroup.IsUnknown() && haveCIDR == haveGroup {
		resp.Diagnostics.AddError("Give one other side", "Set exactly one of cidr or source_group.")
	}
}

func (r *SecurityGroupRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func ruleID(vpc, group string, id int64) string {
	return fmt.Sprintf("%s/%s/%d", vpc, group, id)
}

// fill copies the server's rule into the model. An absent range or group is null, so an omitted attribute stays quiet.
func (m *SecurityGroupRuleResourceModel) fill(vpc, group string, rule SecurityGroupRule) {
	m.ID = types.StringValue(ruleID(vpc, group, rule.ID))
	m.VPC, m.SecurityGroup = types.StringValue(vpc), types.StringValue(group)
	m.RuleID = types.Int64Value(rule.ID)
	m.Direction = types.StringValue(rule.Direction)
	m.Protocol = types.StringValue(rule.Protocol)
	m.PortFrom, m.PortTo = types.Int64Value(rule.PortFrom), types.Int64Value(rule.PortTo)
	m.CIDR, m.SourceGroup = types.StringNull(), types.StringNull()
	if rule.CIDR != "" {
		m.CIDR = types.StringValue(rule.CIDR)
	}
	if rule.Group != "" {
		m.SourceGroup = types.StringValue(rule.Group)
	}
	m.Description = types.StringValue(rule.Description)
}

func (r *SecurityGroupRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecurityGroupRuleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := SecurityGroupRule{
		Direction: plan.Direction.ValueString(), Protocol: plan.Protocol.ValueString(),
		CIDR: plan.CIDR.ValueString(), Group: plan.SourceGroup.ValueString(), Description: plan.Description.ValueString(),
	}
	if !plan.PortFrom.IsNull() && !plan.PortFrom.IsUnknown() {
		in.PortFrom = plan.PortFrom.ValueInt64()
	}
	if !plan.PortTo.IsNull() && !plan.PortTo.IsUnknown() {
		in.PortTo = plan.PortTo.ValueInt64()
	}
	vpc, group := plan.VPC.ValueString(), plan.SecurityGroup.ValueString()
	got, err := r.client.AddSecurityGroupRule(vpc, group, in)
	if err != nil {
		resp.Diagnostics.AddError("Error adding security group rule", err.Error())
		return
	}
	plan.fill(vpc, group, *got)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SecurityGroupRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecurityGroupRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	vpc, group := state.VPC.ValueString(), state.SecurityGroup.ValueString()
	g, err := r.client.GetSecurityGroup(vpc, group)
	if err != nil {
		resp.Diagnostics.AddError("Error reading security group", err.Error())
		return
	}
	if g != nil {
		for _, rule := range g.Rules {
			if rule.ID == state.RuleID.ValueInt64() {
				state.fill(vpc, group, rule)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				return
			}
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *SecurityGroupRuleResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "A rule cannot be changed in place. Changes require replacement.")
}

func (r *SecurityGroupRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecurityGroupRuleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.RemoveSecurityGroupRule(state.VPC.ValueString(), state.SecurityGroup.ValueString(), state.RuleID.ValueInt64()); err != nil {
		resp.Diagnostics.AddError("Error removing security group rule", err.Error())
	}
}

// ImportState takes vpc/group/number.
func (r *SecurityGroupRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected vpc/group/number, got: %s", req.ID))
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("The rule number %q is not a number", parts[2]))
		return
	}
	g, err := r.client.GetSecurityGroup(parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error importing rule", err.Error())
		return
	}
	if g != nil {
		for _, rule := range g.Rules {
			if rule.ID == id {
				var state SecurityGroupRuleResourceModel
				state.fill(parts[0], parts[1], rule)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				return
			}
		}
	}
	resp.Diagnostics.AddError("Rule not found", "No rule "+req.ID)
}
