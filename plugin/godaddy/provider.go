package godaddy

import (
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// Provider returns a terraform.ResourceProvider.
func Provider() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"token": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("GODADDY_API_TOKEN", nil),
				Description: "GoDaddy Personal Access Token. Requires the domains.dns:update and domains.domain:read scopes. Takes precedence over key/secret.",
			},

			"key": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("GODADDY_API_KEY", nil),
				Description: "GoDaddy API Key (deprecated, use token).",
				Deprecated:  "GoDaddy is sunsetting API key/secret pairs. Use token instead.",
			},

			"secret": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("GODADDY_API_SECRET", nil),
				Description: "GoDaddy API Secret (deprecated, use token).",
				Deprecated:  "GoDaddy is sunsetting API key/secret pairs. Use token instead.",
			},

			"baseurl": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "https://api.godaddy.com",
				Description: "GoDaddy Base Url(defaults to production).",
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"godaddy_domain_record": resourceDomainRecord(),
		},

		ConfigureFunc: providerConfigure,
	}
}

func providerConfigure(d *schema.ResourceData) (interface{}, error) {
	config := Config{
		Token:   d.Get("token").(string),
		Key:     d.Get("key").(string),
		Secret:  d.Get("secret").(string),
		BaseURL: d.Get("baseurl").(string),
	}

	if err := config.credentials().Validate(); err != nil {
		return nil, err
	}

	return config.Client()
}
