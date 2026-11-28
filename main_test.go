package main

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// terraform-plugin-mux compares the provider schemas of the two halves and
// reports any difference as an error diagnostic when the schema is first
// requested. This is the test that fails if a description, a sensitivity
// flag or a deprecation in internal/provider drifts from plugin/godaddy.
func TestMuxServesBothProviders(t *testing.T) {
	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		t.Fatalf("muxServer: %s", err)
	}

	resp, err := server().GetProviderSchema(ctx, &tfprotov5.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %s", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov5.DiagnosticSeverityError {
			t.Errorf("schema error: %s: %s", d.Summary, d.Detail)
		}
	}

	if _, ok := resp.ResourceSchemas["godaddy_domain_record"]; !ok {
		t.Error("godaddy_domain_record is not served; the SDKv2 half is missing")
	}
	for _, name := range []string{"token", "key", "secret", "baseurl"} {
		found := false
		for _, a := range resp.Provider.Block.Attributes {
			if a.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("provider attribute %q is missing from the served schema", name)
		}
	}
}

// providerConfig builds the provider block Terraform would send. A nil
// entry means the attribute is not set.
func providerConfig(t *testing.T, values map[string]interface{}) *tfprotov5.DynamicValue {
	t.Helper()
	objectType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"token": tftypes.String, "key": tftypes.String, "secret": tftypes.String, "baseurl": tftypes.String,
	}}
	attrs := map[string]tftypes.Value{}
	for name := range objectType.AttributeTypes {
		attrs[name] = tftypes.NewValue(tftypes.String, values[name])
	}
	dv, err := tfprotov5.NewDynamicValue(objectType, tftypes.NewValue(objectType, attrs))
	if err != nil {
		t.Fatal(err)
	}
	return &dv
}

func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GODADDY_API_TOKEN", "GODADDY_API_KEY", "GODADDY_API_SECRET"} {
		t.Setenv(k, "")
	}
}

// configure runs the two calls Terraform makes to set up a provider and
// returns every diagnostic from both.
func configure(t *testing.T, values map[string]interface{}) []*tfprotov5.Diagnostic {
	t.Helper()
	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := server()
	if _, err := s.GetProviderSchema(ctx, &tfprotov5.GetProviderSchemaRequest{}); err != nil {
		t.Fatal(err)
	}
	config := providerConfig(t, values)

	prepared, err := s.PrepareProviderConfig(ctx, &tfprotov5.PrepareProviderConfigRequest{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := s.ConfigureProvider(ctx, &tfprotov5.ConfigureProviderRequest{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	return append(prepared.Diagnostics, configured.Diagnostics...)
}

func count(diags []*tfprotov5.Diagnostic, severity tfprotov5.DiagnosticSeverity) int {
	n := 0
	for _, d := range diags {
		if d.Severity == severity {
			n++
		}
	}
	return n
}

func TestMuxConfiguresFromEnvironmentAlone(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("GODADDY_API_TOKEN", "tok")

	diags := configure(t, map[string]interface{}{})

	for _, d := range diags {
		t.Errorf("unexpected diagnostic: %s: %s", d.Summary, d.Detail)
	}
}

// With no credentials at all the user must see the error once. The SDKv2
// half reports it; the framework half stays silent on purpose.
func TestMuxReportsMissingCredentialsOnce(t *testing.T) {
	clearCredentialEnv(t)

	diags := configure(t, map[string]interface{}{})

	if n := count(diags, tfprotov5.DiagnosticSeverityError); n != 1 {
		t.Errorf("got %d error diagnostics, want exactly 1: %v", n, diags)
	}
}

// key and secret are deprecated in both halves' schemas, because mux needs
// the schemas to match. This pins how many warnings the user sees for one
// deprecated pair, so a change in either half is noticed.
func TestMuxDeprecationWarningCount(t *testing.T) {
	clearCredentialEnv(t)

	diags := configure(t, map[string]interface{}{"key": "k", "secret": "s"})

	if n := count(diags, tfprotov5.DiagnosticSeverityError); n != 0 {
		t.Fatalf("unexpected errors: %v", diags)
	}
	warnings := count(diags, tfprotov5.DiagnosticSeverityWarning)
	t.Logf("deprecation warnings shown for key and secret: %d", warnings)
	for _, d := range diags {
		t.Logf("  %s: %s (attribute %v)", d.Summary, d.Detail, d.Attribute)
	}
	if warnings != 2 {
		t.Errorf("got %d warnings, want 2 (one for key, one for secret)", warnings)
	}
}
