package godaddy

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

var testAccProviders map[string]*schema.Provider
var testAccProvider *schema.Provider

func init() {
	testAccProvider = Provider()
	testAccProviders = map[string]*schema.Provider{
		"godaddy": testAccProvider,
	}
}

func TestProvider(t *testing.T) {
	if err := Provider().InternalValidate(); err != nil {
		t.Fatalf("err: %s", err)
	}
}

func testAccPreCheck(t *testing.T) {
	hasToken := os.Getenv("GODADDY_API_TOKEN") != ""
	hasKeyPair := os.Getenv("GODADDY_API_KEY") != "" && os.Getenv("GODADDY_API_SECRET") != ""
	if !hasToken && !hasKeyPair {
		t.Fatal("GODADDY_API_TOKEN, or both GODADDY_API_KEY and GODADDY_API_SECRET, must be set for acceptance tests.")
	}
	verifyEnvExists(t, "GODADDY_DOMAIN")
}

func verifyEnvExists(t *testing.T, key string) {
	if v := os.Getenv(key); v == "" {
		t.Fatal(fmt.Sprintf("%s must be set for acceptance tests.", key))
	}
}

// clearCredentialEnv isolates a test from the developer's shell. EnvDefaultFunc
// treats an empty value as unset.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GODADDY_API_TOKEN", "GODADDY_API_KEY", "GODADDY_API_SECRET"} {
		t.Setenv(k, "")
	}
}

func TestProviderConfigureRequiresCredentials(t *testing.T) {
	clearCredentialEnv(t)

	p := Provider()
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{})

	_, err := providerConfigure(d)
	if err == nil {
		t.Fatal("expected error when no credentials are supplied")
	}
	if strings.HasPrefix(err.Error(), "error setting up client") {
		t.Fatalf("expected bare credentials error, got: %s", err)
	}
}

func TestProviderConfigureAcceptsToken(t *testing.T) {
	clearCredentialEnv(t)

	p := Provider()
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{
		"token": "tok",
	})

	client, err := providerConfigure(d)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
}

func TestProviderConfigureAcceptsKeySecret(t *testing.T) {
	clearCredentialEnv(t)
	p := Provider()
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"key": "k", "secret": "s"})
	client, err := providerConfigure(d)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
}

func TestProviderConfigureReadsTokenFromEnv(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("GODADDY_API_TOKEN", "envtok")
	p := Provider()
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{})
	client, err := providerConfigure(d)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
}
