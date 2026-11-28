package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/n3integration/terraform-provider-godaddy/api"
)

func TestResolve(t *testing.T) {
	env := map[string]string{
		"GODADDY_API_TOKEN":  "env-token",
		"GODADDY_API_KEY":    "env-key",
		"GODADDY_API_SECRET": "env-secret",
	}
	getenv := func(k string) string { return env[k] }
	noEnv := func(string) string { return "" }
	null := types.StringNull()

	cases := []struct {
		name   string
		model  providerModel
		getenv func(string) string
		want   Settings
	}{
		{
			name:   "nothing set uses the environment and the default URL",
			model:  providerModel{Token: null, Key: null, Secret: null, BaseURL: null},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name: "configuration wins over the environment",
			model: providerModel{
				Token: types.StringValue("cfg-token"), Key: types.StringValue("cfg-key"),
				Secret: types.StringValue("cfg-secret"), BaseURL: types.StringValue("https://api.ote-godaddy.com"),
			},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "cfg-token", Key: "cfg-key", Secret: "cfg-secret"},
				BaseURL:     "https://api.ote-godaddy.com",
			},
		},
		{
			name:   "an unknown value counts as not set",
			model:  providerModel{Token: types.StringUnknown(), Key: null, Secret: null, BaseURL: types.StringUnknown()},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name:   "an empty string counts as not set",
			model:  providerModel{Token: types.StringValue(""), Key: null, Secret: null, BaseURL: types.StringValue("")},
			getenv: getenv,
			want: Settings{
				Credentials: api.Credentials{Token: "env-token", Key: "env-key", Secret: "env-secret"},
				BaseURL:     DefaultBaseURL,
			},
		},
		{
			name:   "nothing anywhere leaves the credentials empty",
			model:  providerModel{Token: null, Key: null, Secret: null, BaseURL: null},
			getenv: noEnv,
			want:   Settings{BaseURL: DefaultBaseURL},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(tc.model, tc.getenv); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The framework half must use the same default as the SDKv2 half.
func TestDefaultBaseURLMatchesTheOldProvider(t *testing.T) {
	if DefaultBaseURL != "https://api.godaddy.com" {
		t.Errorf("DefaultBaseURL = %q; plugin/godaddy/provider.go defaults baseurl to https://api.godaddy.com", DefaultBaseURL)
	}
}
