//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5/tf5server"
	"github.com/hashicorp/terraform-plugin-mux/tf5muxserver"
	"github.com/n3integration/terraform-provider-godaddy/internal/provider"
	"github.com/n3integration/terraform-provider-godaddy/plugin/godaddy"
)

// providerAddress is where the Terraform Registry serves this provider.
const providerAddress = "registry.terraform.io/jeffbeard/godaddy"

var (
	// these will be set by the goreleaser configuration
	// to appropriate values for the compiled binary
	version string = "dev"

	// goreleaser can also pass the specific commit if you want
	// commit  string = ""
)

// muxServer combines the two halves of the provider into one server:
// the SDKv2 provider, which holds the frozen godaddy_domain_record resource,
// and the framework provider, which holds the new resources.
func muxServer(ctx context.Context) (func() tfprotov5.ProviderServer, error) {
	framework := providerserver.NewProtocol5(provider.New(version)())
	mux, err := tf5muxserver.NewMuxServer(ctx,
		godaddy.Provider().GRPCProvider,
		func() tfprotov5.ProviderServer { return withoutDeprecationWarnings{framework()} },
	)
	if err != nil {
		return nil, err
	}
	return mux.ProviderServer, nil
}

// withoutDeprecationWarnings drops the "Attribute Deprecated" warnings the
// framework half raises for the provider block.
//
// terraform-plugin-mux requires both halves to mark key and secret as
// deprecated, and then passes on the warnings from both, so a user who sets
// them would see every warning twice. The SDKv2 half already reports them.
// Errors, and any other warning, pass through untouched.
type withoutDeprecationWarnings struct {
	tfprotov5.ProviderServer
}

func (w withoutDeprecationWarnings) PrepareProviderConfig(ctx context.Context, req *tfprotov5.PrepareProviderConfigRequest) (*tfprotov5.PrepareProviderConfigResponse, error) {
	resp, err := w.ProviderServer.PrepareProviderConfig(ctx, req)
	if resp == nil {
		return resp, err
	}
	kept := make([]*tfprotov5.Diagnostic, 0, len(resp.Diagnostics))
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov5.DiagnosticSeverityWarning && d.Summary == "Attribute Deprecated" {
			continue
		}
		kept = append(kept, d)
	}
	resp.Diagnostics = kept
	return resp, err
}

func main() {
	debug := flag.Bool("debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	ctx := context.Background()
	server, err := muxServer(ctx)
	if err != nil {
		log.Fatal(err)
	}

	var opts []tf5server.ServeOpt
	if *debug {
		opts = append(opts, tf5server.WithManagedDebug())
	}
	if err := tf5server.Serve(providerAddress, server, opts...); err != nil {
		log.Fatal(err)
	}
}
