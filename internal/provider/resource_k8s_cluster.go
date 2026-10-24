package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &K8sClusterResource{}
	_ resource.ResourceWithImportState = &K8sClusterResource{}
)

// How long to wait for the cluster, its workers and its kubeconfig.
const (
	k8sReadyTimeout      = 60 * time.Minute
	k8sKubeconfigTimeout = 10 * time.Minute
	k8sCredentialTimeout = 8 * time.Minute
)

type K8sClusterResource struct {
	client *VirtuosoClient
}

type K8sClusterResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Size        types.String `tfsdk:"size"`
	DiskGB      types.Int64  `tfsdk:"disk_gb"`
	Bridged     types.Bool   `tfsdk:"bridged"`
	ArgoCD      types.Bool   `tfsdk:"argocd"`
	Headlamp    types.Bool   `tfsdk:"headlamp"`
	K9s         types.Bool   `tfsdk:"k9s"`
	WorkerCount types.Int64  `tfsdk:"worker_count"`
	// Computed
	VMName         types.String `tfsdk:"vm_name"`
	Status         types.String `tfsdk:"status"`
	IP             types.String `tfsdk:"ip"`
	TailscaleIP    types.String `tfsdk:"tailscale_ip"`
	Kubeconfig     types.String `tfsdk:"kubeconfig"`
	ArgoCDPassword types.String `tfsdk:"argocd_password"`
	HeadlampToken  types.String `tfsdk:"headlamp_token"`
}

func NewK8sClusterResource() resource.Resource {
	return &K8sClusterResource{}
}

func (r *K8sClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_k8s_cluster"
}

func (r *K8sClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a k3s Kubernetes cluster (the Kubernetes app) on the virtuOSo server. Requires an admin API key. " +
			"Create waits until the server is running and the worker VMs are up, and returns the kubeconfig. " +
			"Workers join the cluster about a minute after their VM is up, so use kubectl to confirm nodes are Ready before scheduling work.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "Cluster name.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Description:   "Cluster name. The VM is named k8s-<name>. Changing it recreates the cluster.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"size": schema.StringAttribute{
				Description: "Instance size of the server node (see the virtuoso sizes, for example medium or large). Changing it recreates the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("medium"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"disk_gb": schema.Int64Attribute{
				Description: "Disk size in GB. 0 uses the size's default. Changing it recreates the cluster.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"bridged": schema.BoolAttribute{
				Description: "Use bridged networking instead of NAT (on-prem only). Changing it recreates the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"argocd": schema.BoolAttribute{
				Description: "Install ArgoCD. Can be turned on later; it cannot be removed without recreating the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"headlamp": schema.BoolAttribute{
				Description: "Install Headlamp. Can be turned on later; it cannot be removed without recreating the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"k9s": schema.BoolAttribute{
				Description: "Install k9s. Can be turned on later; it cannot be removed without recreating the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"worker_count": schema.Int64Attribute{
				Description: "Number of worker nodes, 0 to 10. Changing it scales the cluster in place; scaling down drains and removes the newest workers. Apply returns once the worker VMs are up, before they have joined the cluster.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
			},
			"vm_name": schema.StringAttribute{
				Description:   "Name of the server VM.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Description: "provisioning or running.",
				Computed:    true,
			},
			"ip": schema.StringAttribute{
				Description:   "IP address of the server node.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tailscale_ip": schema.StringAttribute{
				Description: "Tailscale IP of the server node, when Tailscale is configured on the server.",
				Computed:    true,
			},
			"kubeconfig": schema.StringAttribute{
				Description:   "Admin kubeconfig for the cluster. Stored in the Terraform state, so protect the state file.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"argocd_password": schema.StringAttribute{
				Description:   "ArgoCD admin password, once ArgoCD has finished installing.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"headlamp_token": schema.StringAttribute{
				Description:   "Headlamp login token, once Headlamp has finished installing.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *K8sClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// mapK8sCluster copies what the API reports into the model. Config-only values
// the API does not return (bridged) are left as they are.
func mapK8sCluster(cl *K8sClusterResponse, m *K8sClusterResourceModel) {
	m.ID = types.StringValue(cl.Name)
	m.Name = types.StringValue(cl.Name)
	m.VMName = types.StringValue(cl.VMName)
	m.Status = types.StringValue(cl.Status)
	m.IP = types.StringValue(cl.IP)
	m.TailscaleIP = types.StringValue(cl.TailscaleIP)
	m.Size = types.StringValue(cl.Size)
	m.DiskGB = types.Int64Value(cl.DiskGB)
	m.ArgoCD = types.BoolValue(cl.ArgoCD)
	m.Headlamp = types.BoolValue(cl.Headlamp)
	m.K9s = types.BoolValue(cl.K9s)
	m.WorkerCount = types.Int64Value(int64(len(cl.Workers)))
}

// fillSecrets sets the sensitive computed values that are still unknown or empty.
// A missing kubeconfig or credential is not an error: it is filled on a later refresh.
func (r *K8sClusterResource) fillSecrets(ctx context.Context, m *K8sClusterResourceModel, wait bool) {
	name := m.Name.ValueString()

	if m.Kubeconfig.IsNull() || m.Kubeconfig.IsUnknown() || m.Kubeconfig.ValueString() == "" {
		var kc string
		var err error
		if wait {
			kc, err = r.client.WaitForK8sKubeconfig(name, k8sKubeconfigTimeout)
		} else {
			kc, err = r.client.GetK8sKubeconfig(name)
		}
		if err != nil {
			tflog.Warn(ctx, "kubeconfig not available yet", map[string]interface{}{"cluster": name, "error": err.Error()})
			kc = ""
		}
		m.Kubeconfig = types.StringValue(kc)
	}

	needArgo := m.ArgoCD.ValueBool() && (m.ArgoCDPassword.IsNull() || m.ArgoCDPassword.IsUnknown() || m.ArgoCDPassword.ValueString() == "")
	needLamp := m.Headlamp.ValueBool() && (m.HeadlampToken.IsNull() || m.HeadlampToken.IsUnknown() || m.HeadlampToken.ValueString() == "")
	if !needArgo && !needLamp {
		if m.ArgoCDPassword.IsNull() || m.ArgoCDPassword.IsUnknown() {
			m.ArgoCDPassword = types.StringValue("")
		}
		if m.HeadlampToken.IsNull() || m.HeadlampToken.IsUnknown() {
			m.HeadlampToken = types.StringValue("")
		}
		return
	}
	deadline := time.Now().Add(k8sCredentialTimeout)
	for {
		creds, err := r.client.GetK8sCredentials(name)
		if err == nil {
			if needArgo && creds.ArgoCDPassword != "" {
				m.ArgoCDPassword = types.StringValue(creds.ArgoCDPassword)
				needArgo = false
			}
			if needLamp && creds.HeadlampToken != "" {
				m.HeadlampToken = types.StringValue(creds.HeadlampToken)
				needLamp = false
			}
		}
		if (!needArgo && !needLamp) || !wait || time.Now().After(deadline) {
			break
		}
		time.Sleep(appPollInterval)
	}
	if m.ArgoCDPassword.IsNull() || m.ArgoCDPassword.IsUnknown() {
		m.ArgoCDPassword = types.StringValue("")
	}
	if m.HeadlampToken.IsNull() || m.HeadlampToken.IsUnknown() {
		m.HeadlampToken = types.StringValue("")
	}
}

func (r *K8sClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan K8sClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	create := K8sCreateRequest{
		Name:        plan.Name.ValueString(),
		Size:        plan.Size.ValueString(),
		Bridged:     plan.Bridged.ValueBool(),
		ArgoCD:      plan.ArgoCD.ValueBool(),
		Headlamp:    plan.Headlamp.ValueBool(),
		K9s:         plan.K9s.ValueBool(),
		WorkerCount: plan.WorkerCount.ValueInt64(),
	}
	if !plan.DiskGB.IsNull() && !plan.DiskGB.IsUnknown() {
		create.DiskGB = plan.DiskGB.ValueInt64()
	}

	tflog.Info(ctx, "Creating Kubernetes cluster", map[string]interface{}{"name": create.Name})
	cl, err := r.client.CreateK8sCluster(create)
	if err != nil {
		resp.Diagnostics.AddError("Error creating Kubernetes cluster", err.Error())
		return
	}

	// Record the cluster in state right away so a timeout leaves it tracked
	// (tainted) instead of orphaned on the server.
	mapK8sCluster(cl, &plan)
	plan.Kubeconfig = types.StringValue("")
	plan.ArgoCDPassword = types.StringValue("")
	plan.HeadlampToken = types.StringValue("")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ready, err := r.client.WaitForK8sCluster(plan.Name.ValueString(), plan.WorkerCount.ValueInt64(), k8sReadyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes cluster", err.Error())
		return
	}
	mapK8sCluster(ready, &plan)
	r.fillSecrets(ctx, &plan, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *K8sClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state K8sClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cl, err := r.client.GetK8sCluster(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading Kubernetes cluster", err.Error())
		return
	}
	if cl == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	mapK8sCluster(cl, &state)
	if cl.Status == "running" {
		r.fillSecrets(ctx, &state, false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *K8sClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state K8sClusterResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()

	for _, a := range []struct {
		name         string
		plan, before types.Bool
	}{
		{"argocd", plan.ArgoCD, state.ArgoCD},
		{"headlamp", plan.Headlamp, state.Headlamp},
		{"k9s", plan.K9s, state.K9s},
	} {
		if a.before.ValueBool() && !a.plan.ValueBool() {
			resp.Diagnostics.AddError("Cannot remove add-on",
				fmt.Sprintf("The %s add-on cannot be removed from an existing cluster. Set %s = true again, or recreate the cluster.", a.name, a.name))
			return
		}
		if !a.before.ValueBool() && a.plan.ValueBool() {
			if err := r.client.InstallK8sAddon(name, a.name); err != nil {
				resp.Diagnostics.AddError("Error installing add-on "+a.name, err.Error())
				return
			}
		}
	}

	if plan.WorkerCount.ValueInt64() != state.WorkerCount.ValueInt64() {
		if err := r.client.ScaleK8sCluster(name, plan.WorkerCount.ValueInt64()); err != nil {
			resp.Diagnostics.AddError("Error scaling Kubernetes cluster", err.Error())
			return
		}
	}

	cl, err := r.client.WaitForK8sCluster(name, plan.WorkerCount.ValueInt64(), k8sReadyTimeout)
	if err != nil {
		resp.Diagnostics.AddError("Error waiting for Kubernetes cluster", err.Error())
		return
	}
	mapK8sCluster(cl, &plan)
	plan.Kubeconfig = state.Kubeconfig
	plan.ArgoCDPassword = state.ArgoCDPassword
	plan.HeadlampToken = state.HeadlampToken
	r.fillSecrets(ctx, &plan, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *K8sClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state K8sClusterResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tflog.Info(ctx, "Deleting Kubernetes cluster", map[string]interface{}{"name": state.Name.ValueString()})
	if err := r.client.DeleteK8sCluster(state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting Kubernetes cluster", err.Error())
	}
}

func (r *K8sClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	cl, err := r.client.GetK8sCluster(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error importing Kubernetes cluster", err.Error())
		return
	}
	if cl == nil {
		resp.Diagnostics.AddError("Cluster not found", "No Kubernetes cluster named "+req.ID)
		return
	}
	state := K8sClusterResourceModel{
		Bridged:        types.BoolValue(false),
		Kubeconfig:     types.StringValue(""),
		ArgoCDPassword: types.StringValue(""),
		HeadlampToken:  types.StringValue(""),
	}
	mapK8sCluster(cl, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
