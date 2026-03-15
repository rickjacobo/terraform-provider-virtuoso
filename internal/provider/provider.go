package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &VirtuosoProvider{}

type VirtuosoProvider struct {
	version string
}

type VirtuosoProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIKey   types.String `tfsdk:"api_key"`
	Insecure types.Bool   `tfsdk:"insecure"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &VirtuosoProvider{version: version}
	}
}

func (p *VirtuosoProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "virtuoso"
	resp.Version = p.version
}

func (p *VirtuosoProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Terraform provider for managing virtuOSo VMs, SSH keys, and access control.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Description: "virtuOSo server URL (e.g. https://192.168.33.99). Can also be set with VIRTUOSO_ENDPOINT.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "API key for authentication (vmk_...). Can also be set with VIRTUOSO_API_KEY.",
				Optional:    true,
				Sensitive:   true,
			},
			"insecure": schema.BoolAttribute{
				Description: "Skip TLS certificate verification (for self-signed certs). Can also be set with VIRTUOSO_INSECURE.",
				Optional:    true,
			},
		},
	}
}

func (p *VirtuosoProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config VirtuosoProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := os.Getenv("VIRTUOSO_ENDPOINT")
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	if endpoint == "" {
		resp.Diagnostics.AddError("Missing endpoint", "Set the endpoint attribute or VIRTUOSO_ENDPOINT environment variable.")
		return
	}

	apiKey := os.Getenv("VIRTUOSO_API_KEY")
	if !config.APIKey.IsNull() {
		apiKey = config.APIKey.ValueString()
	}
	if apiKey == "" {
		resp.Diagnostics.AddError("Missing API key", "Set the api_key attribute or VIRTUOSO_API_KEY environment variable.")
		return
	}

	insecure := os.Getenv("VIRTUOSO_INSECURE") == "true" || os.Getenv("VIRTUOSO_INSECURE") == "1"
	if !config.Insecure.IsNull() {
		insecure = config.Insecure.ValueBool()
	}

	client := NewVirtuosoClient(endpoint, apiKey, insecure)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *VirtuosoProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVMResource,
		NewSSHKeyResource,
		NewVMAccessResource,
	}
}

func (p *VirtuosoProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewVMDataSource,
		NewVMsDataSource,
	}
}
