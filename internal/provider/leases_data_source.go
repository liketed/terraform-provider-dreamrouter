package provider

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var _ datasource.DataSourceWithConfigure = &leasesDataSource{}

type leasesDataSource struct {
	data *providerData
}

type leaseModel struct {
	IP         types.String `tfsdk:"ip"`
	MAC        types.String `tfsdk:"mac"`
	Name       types.String `tfsdk:"name"`
	Hostname   types.String `tfsdk:"hostname"`
	Vendor     types.String `tfsdk:"vendor"`
	Status     types.String `tfsdk:"status"`
	Connection types.String `tfsdk:"connection"`
	Expires    types.String `tfsdk:"expires"`
	Reserved   types.Bool   `tfsdk:"reserved"`
	DNSName    types.String `tfsdk:"dns_name"`
	NetworkID  types.String `tfsdk:"network_id"`
}

type leasesModel struct {
	Network types.String `tfsdk:"network"`
	Status  types.String `tfsdk:"status"`
	Leases  []leaseModel `tfsdk:"leases"`
}

func newLeasesDataSource() datasource.DataSource { return &leasesDataSource{} }

func (d *leasesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_leases"
}

func (d *leasesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the router's current DHCP leases: which device has which address. Leases describe the " +
			"network right now (expiry times change constantly, devices come and go), so use this for lookups, " +
			"e.g. finding a device's MAC address to reserve it, rather than feeding changing values into resources.",
		Attributes: map[string]schema.Attribute{
			"network": schema.StringAttribute{
				Optional:    true,
				Description: "Only return leases on the network with this name (case-insensitive).",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Description: `Only return leases whose device is "online" or "offline".`,
				Validators:  []validator.String{stringvalidator.OneOf("online", "offline")},
			},
			"leases": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Matching leases, sorted by IP address.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"ip":         str("IPv4 address the device has."),
						"mac":        str("The device's MAC address, in lower-case colon form."),
						"name":       str("The device's name: the one set in the web UI, else the router's name for it, else its host name."),
						"hostname":   str("The host name the device sent with its DHCP request; may be empty."),
						"vendor":     str("Manufacturer, from the MAC address; may be empty."),
						"status":     str(`"online" or "offline".`),
						"connection": str(`"wired" or "wireless"; may be empty.`),
						"expires":    str("When the lease expires, as an RFC 3339 timestamp (UTC); empty if unknown."),
						"reserved":   schema.BoolAttribute{Computed: true, Description: "Whether the device has a DHCP reservation."},
						"dns_name":   str("The device's DNS name (see dreamrouter_host); empty if none."),
						"network_id": str("ID of the network the lease is on."),
					},
				},
			},
		},
	}
}

func (d *leasesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *leasesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg leasesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	leases, err := d.data.client.ListLeases(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing DHCP leases", err.Error())
		return
	}
	networkID := ""
	if !cfg.Network.IsNull() {
		networks, err := d.data.client.ListNetworks(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Error listing networks", err.Error())
			return
		}
		var lans []unifi.Network
		for _, n := range networks {
			if n.Subnet == "" {
				continue
			}
			lans = append(lans, n)
			if strings.EqualFold(n.Name, cfg.Network.ValueString()) {
				networkID = n.ID
			}
		}
		if networkID == "" {
			resp.Diagnostics.AddAttributeError(path.Root("network"), "Network not found",
				fmt.Sprintf("The router has no network named %q (networks: %s).", cfg.Network.ValueString(), check.NetworkNames(lans)))
			return
		}
	}
	sort.SliceStable(leases, func(i, j int) bool {
		a, errA := netip.ParseAddr(leases[i].IP)
		b, errB := netip.ParseAddr(leases[j].IP)
		if errA != nil || errB != nil {
			return leases[i].IP < leases[j].IP
		}
		return a.Less(b)
	})
	cfg.Leases = []leaseModel{}
	for _, l := range leases {
		status := strings.ToLower(l.Status)
		if networkID != "" && l.NetworkID != networkID {
			continue
		}
		if !cfg.Status.IsNull() && status != cfg.Status.ValueString() {
			continue
		}
		expires := ""
		if t := l.Expires(); !t.IsZero() {
			expires = t.UTC().Format(time.RFC3339)
		}
		cfg.Leases = append(cfg.Leases, leaseModel{
			IP: types.StringValue(l.IP), MAC: types.StringValue(l.MAC), Name: types.StringValue(l.Label()),
			Hostname: types.StringValue(l.Hostname), Vendor: types.StringValue(l.OUI), Status: types.StringValue(status),
			Connection: types.StringValue(strings.ToLower(l.ClientType)), Expires: types.StringValue(expires),
			Reserved: types.BoolValue(l.UseFixedIP), DNSName: types.StringValue(l.LocalDNSRecord),
			NetworkID: types.StringValue(l.NetworkID),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
