// Package provider implements the dreamrouter Terraform provider, which manages
// static DNS records on a UniFi Dream Router 7 or other UniFi OS gateway.
package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/liketed/dreamrouter-go/unifi"
)

const (
	defaultHost     = "192.168.1.1"
	defaultSite     = "default"
	defaultUsername = "admin"

	// Long enough for the router's per-minute login limit to reset.
	defaultLoginRetryTimeout = 2 * time.Minute
)

type dreamRouterProvider struct {
	version string
}

type providerModel struct {
	Host     types.String `tfsdk:"host"`
	Site     types.String `tfsdk:"site"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Insecure types.Bool   `tfsdk:"insecure"`

	LoginRetryTimeout types.String `tfsdk:"login_retry_timeout"`
}

// providerData is what resources and data sources receive from Configure.
type providerData struct {
	client         *unifi.Client
	reportedWaited atomic.Bool
}

// warnIfLoginWaited adds a warning, once per provider run, if the client had to
// wait for the router's login limit, so it shows in normal plan/apply output.
func (p *providerData) warnIfLoginWaited(diags *diag.Diagnostics) {
	waited := p.client.LoginWaited()
	if waited > 0 && p.reportedWaited.CompareAndSwap(false, true) {
		diags.AddWarning("Router login limit reached",
			fmt.Sprintf("The router refused to log in because its login limit was reached, so the provider "+
				"waited %s before retrying. The limit is set by success.login.limit.count in "+
				"/usr/lib/ulp-go/config.props on the router (default 5 logins per minute).", waited.Round(time.Second)))
	}
}

// New returns a constructor for the provider, as providerserver expects.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &dreamRouterProvider{version: version} }
}

func (p *dreamRouterProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "dreamrouter"
	resp.Version = p.version
}

func (p *dreamRouterProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages static DNS records, DHCP reservations, device DNS names and network DHCP settings on a UniFi Dream " +
			"Router 7 (or other UniFi OS gateway) through the UniFi Network application's API: the same " +
			"settings as Settings → Routing → DNS and each client's fixed IP and local DNS record in the web UI.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional: true,
				Description: "Router address, optionally with a port. Defaults to the DREAMROUTER_HOST " +
					"environment variable, then " + defaultHost + ".",
			},
			"site": schema.StringAttribute{
				Optional:    true,
				Description: "UniFi Network site name. Defaults to \"" + defaultSite + "\".",
			},
			"username": schema.StringAttribute{
				Optional: true,
				Description: "Local UniFi OS account (not a UI.com SSO account). Defaults to the " +
					"DREAMROUTER_USERNAME or UNIFI_USER environment variable, then \"" + defaultUsername + "\".",
			},
			"password": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Password for the account. Defaults to the DREAMROUTER_PASSWORD or UNIFI_PASS " +
					"environment variable. Prefer the environment variable to keep it out of configuration.",
			},
			"login_retry_timeout": schema.StringAttribute{
				Optional: true,
				Description: "How long to keep retrying when the router refuses to log in because its login " +
					"limit has been reached (HTTP 429), as a duration such as \"2m\" or \"90s\". \"0\" fails " +
					"immediately. Defaults to the DREAMROUTER_LOGIN_RETRY_TIMEOUT environment variable, " +
					"then \"2m\".",
			},
			"insecure": schema.BoolAttribute{
				Optional: true,
				Description: "Skip TLS certificate verification. Defaults to true (or DREAMROUTER_INSECURE), " +
					"because the router uses a self-signed certificate out of the box.",
			},
		},
	}
}

func (p *dreamRouterProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]interface{ IsUnknown() bool }{
		"host": cfg.Host, "site": cfg.Site, "username": cfg.Username, "password": cfg.Password, "insecure": cfg.Insecure,
		"login_retry_timeout": cfg.LoginRetryTimeout,
	} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration",
				"The "+name+" setting must be known when the provider is configured; it cannot depend on "+
					"values that are only known after apply.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	insecure := true
	if v, err := strconv.ParseBool(os.Getenv("DREAMROUTER_INSECURE")); err == nil {
		insecure = v
	}
	if !cfg.Insecure.IsNull() {
		insecure = cfg.Insecure.ValueBool()
	}
	retryTimeout := defaultLoginRetryTimeout
	if s := firstNonEmpty(cfg.LoginRetryTimeout.ValueString(), os.Getenv("DREAMROUTER_LOGIN_RETRY_TIMEOUT")); s != "" {
		d, err := parseTimeout(s)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("login_retry_timeout"), "Invalid login_retry_timeout",
				fmt.Sprintf("%q is not a valid duration: use e.g. \"2m\", \"90s\", or \"0\" to disable retrying.", s))
			return
		}
		retryTimeout = d
	}
	password := firstNonEmpty(cfg.Password.ValueString(), os.Getenv("DREAMROUTER_PASSWORD"), os.Getenv("UNIFI_PASS"))
	if password == "" {
		resp.Diagnostics.AddAttributeError(path.Root("password"), "Missing router password",
			"Set the DREAMROUTER_PASSWORD environment variable (or UNIFI_PASS), or the provider's password argument.")
		return
	}

	c, err := unifi.New(unifi.Config{
		Host:               firstNonEmpty(cfg.Host.ValueString(), os.Getenv("DREAMROUTER_HOST"), defaultHost),
		Site:               firstNonEmpty(cfg.Site.ValueString(), defaultSite),
		Username:           firstNonEmpty(cfg.Username.ValueString(), os.Getenv("DREAMROUTER_USERNAME"), os.Getenv("UNIFI_USER"), defaultUsername),
		Password:           password,
		InsecureSkipVerify: insecure,
		LoginRetryTimeout:  retryTimeout,
		CacheTTL:           30 * time.Second, // one plan/apply's reads share a record list
		Logf: func(ctx context.Context, msg string) {
			tflog.Warn(ctx, msg)
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	data := &providerData{client: c}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *dreamRouterProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newRecordResource, newReservationResource, newHostResource, newNetworkDHCPResource}
}

func (p *dreamRouterProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{newRecordsDataSource, newNetworksDataSource, newLeasesDataSource}
}

// parseTimeout accepts Go durations ("2m", "90s") and a bare "0".
func parseTimeout(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err == nil && d < 0 {
		err = fmt.Errorf("negative duration")
	}
	return d, err
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
