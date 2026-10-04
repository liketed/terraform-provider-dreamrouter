package provider

import (
	"context"
	"fmt"
	"github.com/liketed/dreamrouter-go/unifi"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &networksDataSource{}

var pathName = path.Root("name")

type networksDataSource struct {
	data *providerData
}

type networkModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Purpose     types.String `tfsdk:"purpose"`
	Subnet      types.String `tfsdk:"subnet"`
	VLAN        types.String `tfsdk:"vlan"`
	DHCPEnabled types.Bool   `tfsdk:"dhcp_enabled"`
	DHCPStart   types.String `tfsdk:"dhcp_start"`
	DHCPStop    types.String `tfsdk:"dhcp_stop"`
	DomainName  types.String `tfsdk:"domain_name"`
}

type networksModel struct {
	Name     types.String   `tfsdk:"name"`
	Networks []networkModel `tfsdk:"networks"`
}

func newNetworksDataSource() datasource.DataSource { return &networksDataSource{} }

func (d *networksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_networks"
}

func (d *networksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the router's networks (Settings → Networks), e.g. to find the network_id for a DHCP reservation.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Only return the network with this name (case-insensitive).",
			},
			"networks": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Matching networks, sorted by name.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":           str("Network ID, for network_id on dreamrouter_dhcp_reservation and dreamrouter_host."),
						"name":         str("Network name."),
						"purpose":      str(`Purpose, e.g. "corporate", "guest" or "wan".`),
						"subnet":       str("The router's address with prefix length, e.g. 192.168.1.1/24; empty for WAN networks."),
						"vlan":         str("VLAN ID, if any."),
						"dhcp_enabled": schema.BoolAttribute{Computed: true, Description: "Whether the router's DHCP server is enabled."},
						"dhcp_start":   str("First address of the DHCP pool."),
						"dhcp_stop":    str("Last address of the DHCP pool."),
						"domain_name":  str("Domain name handed out by DHCP."),
					},
				},
			},
		},
	}
}

func (d *networksDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	d.data = data
}

func (d *networksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg networksModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	networks, err := d.data.client.ListNetworks(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing networks", err.Error())
		return
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	cfg.Networks = []networkModel{}
	for _, n := range networks {
		if !cfg.Name.IsNull() && !strings.EqualFold(n.Name, cfg.Name.ValueString()) {
			continue
		}
		cfg.Networks = append(cfg.Networks, networkModel{
			ID: types.StringValue(n.ID), Name: types.StringValue(n.Name), Purpose: types.StringValue(n.Purpose),
			Subnet: types.StringValue(n.Subnet), VLAN: types.StringValue(vlanText(n)), DHCPEnabled: types.BoolValue(n.DHCPEnabled),
			DHCPStart: types.StringValue(n.DHCPStart), DHCPStop: types.StringValue(n.DHCPStop), DomainName: types.StringValue(n.DomainName),
		})
	}
	if !cfg.Name.IsNull() && len(cfg.Networks) == 0 {
		resp.Diagnostics.AddAttributeError(pathName, "Network not found", fmt.Sprintf("The router has no network named %q.", cfg.Name.ValueString()))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// vlanText is a network's VLAN ID as text, empty for the untagged LAN.
func vlanText(n unifi.Network) string {
	if !n.VLANEnabled || n.VLAN == 0 {
		return ""
	}
	return fmt.Sprint(int(n.VLAN))
}
