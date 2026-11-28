// Package provider is the terraform-plugin-framework half of the GoDaddy
// provider. It is served beside the older SDKv2 provider in plugin/godaddy
// through terraform-plugin-mux, so both are configured from one provider
// block. New resources are added here; plugin/godaddy is frozen.
package provider

import (
	"context"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/n3integration/terraform-provider-godaddy/api"
)

// DefaultBaseURL is used when baseurl is not set. It must equal the default
// in plugin/godaddy/provider.go.
const DefaultBaseURL = "https://api.godaddy.com"

// The descriptions and deprecation messages below must stay identical to
// plugin/godaddy/provider.go. terraform-plugin-mux refuses to serve two
// providers whose schemas differ; TestMuxServesBothProviders checks this.
const (
	tokenDescription   = "GoDaddy Personal Access Token. Requires the domains.dns:update and domains.domain:read scopes. Takes precedence over key/secret."
	keyDescription     = "GoDaddy API Key (deprecated, use token)."
	secretDescription  = "GoDaddy API Secret (deprecated, use token)."
	baseURLDescription = "GoDaddy Base Url(defaults to production)."
	deprecationMessage = "GoDaddy is sunsetting API key/secret pairs. Use token instead."
)

type godaddyProvider struct {
	version string
}

// New returns a constructor for the framework provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &godaddyProvider{version: version}
	}
}

type providerModel struct {
	Token   types.String `tfsdk:"token"`
	Key     types.String `tfsdk:"key"`
	Secret  types.String `tfsdk:"secret"`
	BaseURL types.String `tfsdk:"baseurl"`
}

func (p *godaddyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "godaddy"
	resp.Version = p.version
}

func (p *godaddyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: tokenDescription,
			},
			"key": schema.StringAttribute{
				Optional:           true,
				Sensitive:          true,
				Description:        keyDescription,
				DeprecationMessage: deprecationMessage,
			},
			"secret": schema.StringAttribute{
				Optional:           true,
				Sensitive:          true,
				Description:        secretDescription,
				DeprecationMessage: deprecationMessage,
			},
			"baseurl": schema.StringAttribute{
				Optional:    true,
				Description: baseURLDescription,
			},
		},
	}
}

// Settings is the provider configuration after environment variables and
// defaults have been applied.
type Settings struct {
	Credentials api.Credentials
	BaseURL     string
}

// resolve applies the same rules as the SDKv2 provider: a value set in the
// provider block wins; otherwise the environment variable is used; baseurl
// falls back to DefaultBaseURL. An unknown or null value counts as not set.
func resolve(m providerModel, getenv func(string) string) Settings {
	pick := func(v types.String, env string) string {
		if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			return v.ValueString()
		}
		return getenv(env)
	}
	s := Settings{
		Credentials: api.Credentials{
			Token:  pick(m.Token, "GODADDY_API_TOKEN"),
			Key:    pick(m.Key, "GODADDY_API_KEY"),
			Secret: pick(m.Secret, "GODADDY_API_SECRET"),
		},
		BaseURL: DefaultBaseURL,
	}
	if !m.BaseURL.IsNull() && !m.BaseURL.IsUnknown() && strings.TrimSpace(m.BaseURL.ValueString()) != "" {
		s.BaseURL = m.BaseURL.ValueString()
	}
	return s
}

// Configure builds the API client that resources in this provider use.
//
// It reports no error of its own when the credentials or base URL are bad.
// The SDKv2 provider is configured by the same call from Terraform and
// already reports that error; reporting it here too would show it twice.
// In that case no client is stored, and a resource that needs one must say
// so (see ClientFrom).
func (p *godaddyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings := resolve(m, os.Getenv)
	client, err := api.NewClient(settings.BaseURL, settings.Credentials)
	if err != nil {
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *godaddyProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *godaddyProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
