// Command terraform-provider-dreamrouter is a Terraform provider for managing
// static DNS records on a UniFi Dream Router 7 or other UniFi OS gateway.
package main

// Regenerate docs/ from the provider schema, examples/ and templates/:
//
//	go generate ./...
//
//go:generate terraform fmt -recursive ./examples/
//go:generate go tool tfplugindocs generate --provider-name dreamrouter --rendered-provider-name "Dream Router"

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/liketed/terraform-provider-dreamrouter/internal/provider"
)

// version is set at build time with -ldflags "-X main.version=1.2.3".
var version = "dev"

// address is the provider's source address: use source = "liketed/dreamrouter"
// in required_providers (see README.md for installing a local build).
const address = "registry.terraform.io/liketed/dreamrouter"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: address,
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
