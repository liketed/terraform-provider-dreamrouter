package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var _ datasource.DataSourceWithConfigure = &clientsDataSource{}

type clientsDataSource struct {
	data *providerData
}

type clientModel struct {
	MAC           types.String `tfsdk:"mac"`
	Name          types.String `tfsdk:"name"`
	Hostname      types.String `tfsdk:"hostname"`
	IP            types.String `tfsdk:"ip"`
	Status        types.String `tfsdk:"status"`
	Connection    types.String `tfsdk:"connection"`
	NetworkID     types.String `tfsdk:"network_id"`
	SSID          types.String `tfsdk:"ssid"`
	Band          types.String `tfsdk:"band"`
	Signal        types.Int64  `tfsdk:"signal"`
	Uplink        types.String `tfsdk:"uplink"`
	Port          types.Int64  `tfsdk:"port"`
	LinkMbps      types.Int64  `tfsdk:"link_mbps"`
	Uptime        types.Int64  `tfsdk:"uptime"`
	DownloadBytes types.Int64  `tfsdk:"download_bytes"`
	UploadBytes   types.Int64  `tfsdk:"upload_bytes"`
	Vendor        types.String `tfsdk:"vendor"`
	LastSeen      types.String `tfsdk:"last_seen"`
	Blocked       types.Bool   `tfsdk:"blocked"`
	Reserved      types.Bool   `tfsdk:"reserved"`
	DNSName       types.String `tfsdk:"dns_name"`
	Note          types.String `tfsdk:"note"`
}

type clientsModel struct {
	Status     types.String  `tfsdk:"status"`
	Connection types.String  `tfsdk:"connection_type"`
	Blocked    types.Bool    `tfsdk:"blocked"`
	Network    types.String  `tfsdk:"network"`
	Days       types.Int64   `tfsdk:"days"`
	Clients    []clientModel `tfsdk:"clients"`
}

func newClientsDataSource() datasource.DataSource { return &clientsDataSource{} }

func (d *clientsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_clients"
}

func (d *clientsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the devices (clients) on the network: those connected now, those seen within `days`, and " +
			"blocked devices however long ago they were seen. Like leases, this describes the network right now " +
			"(uptime, traffic and signal change constantly), so use it for lookups, e.g. finding a device's MAC address " +
			"to block or reserve it, rather than feeding changing values into resources.",
		Attributes: map[string]schema.Attribute{
			"status": schema.StringAttribute{
				Optional:    true,
				Description: `Only return devices that are "online" or "offline".`,
				Validators:  []validator.String{stringvalidator.OneOf("online", "offline")},
			},
			"connection_type": schema.StringAttribute{
				Optional:    true,
				Description: `Only return "wired" or "wifi" devices. Devices the router has only a stored record for (never connected, or not seen within days) have no connection and are left out.`,
				Validators:  []validator.String{stringvalidator.OneOf("wired", "wifi")},
			},
			"blocked": schema.BoolAttribute{
				Optional:    true,
				Description: "Only return blocked (true) or unblocked (false) devices.",
			},
			"network": schema.StringAttribute{
				Optional:    true,
				Description: "Only return devices on the network with this name (case-insensitive).",
			},
			"days": schema.Int64Attribute{
				Optional:    true,
				Description: "How many days back to look for offline devices. Defaults to 7.",
				Validators:  []validator.Int64{int64validator.Between(1, 365)},
			},
			"clients": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Matching devices, sorted by IP address (devices without one last).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"mac":            str("The device's MAC address, in lower-case colon form."),
						"name":           str("The device's name: the one set in the web UI, else the router's name for it, else its host name; may be empty."),
						"hostname":       str("The host name the device sent; may be empty."),
						"ip":             str("The device's IP address: its current one, else its last one, else its reserved one; may be empty."),
						"status":         str(`"online" or "offline".`),
						"connection":     str(`"wired" or "wifi"; empty if the device hasn't been seen recently.`),
						"network_id":     str("ID of the network the device is on; may be empty."),
						"ssid":           str("Wi-Fi network name; empty for wired devices."),
						"band":           str(`Wi-Fi band: "2.4GHz", "5GHz" or "6GHz"; empty for wired devices.`),
						"signal":         num("Wi-Fi signal strength in dBm (e.g. -61); 0 for wired or offline devices."),
						"uplink":         str("The access point, switch or router the device connects through."),
						"port":           num("Switch or router port of a wired device; 0 if unknown."),
						"link_mbps":      num("Link speed of a wired device in Mbit/s; 0 if unknown."),
						"uptime":         num("Seconds the device has been connected; 0 if offline."),
						"download_bytes": num("Bytes the device has downloaded."),
						"upload_bytes":   num("Bytes the device has uploaded."),
						"vendor":         str("Manufacturer, from the MAC address; may be empty."),
						"last_seen":      str("When the device was last seen, as an RFC 3339 timestamp (UTC); empty if never."),
						"blocked":        schema.BoolAttribute{Computed: true, Description: "Whether the device is blocked."},
						"reserved":       schema.BoolAttribute{Computed: true, Description: "Whether the device has a DHCP reservation."},
						"dns_name":       str("The device's DNS name (see dreamrouter_host); empty if none."),
						"note":           str("The note set on the device in the web UI; empty if none."),
					},
				},
			},
		},
	}
}

func (d *clientsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *clientsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg clientsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	days := int64(7)
	if !cfg.Days.IsNull() {
		days = cfg.Days.ValueInt64()
	}
	clients, err := loadClients(ctx, d.data.client, int(days*24), false)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing devices", err.Error())
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

	cfg.Clients = []clientModel{}
	for _, ci := range clients {
		m := clientModel{
			MAC: types.StringValue(ci.mac()), Name: types.StringValue(ci.name()), IP: types.StringValue(ci.ip()),
			Status: types.StringValue("offline"), Blocked: types.BoolValue(ci.blocked()), Reserved: types.BoolValue(false),
			Hostname: types.StringValue(""), Connection: types.StringValue(""), NetworkID: types.StringValue(""),
			SSID: types.StringValue(""), Band: types.StringValue(""), Uplink: types.StringValue(""), Vendor: types.StringValue(""),
			DNSName: types.StringValue(""), Note: types.StringValue(""), LastSeen: types.StringValue(""),
			Signal: types.Int64Value(0), Port: types.Int64Value(0), LinkMbps: types.Int64Value(0), Uptime: types.Int64Value(0),
			DownloadBytes: types.Int64Value(0), UploadBytes: types.Int64Value(0),
		}
		if t := ci.lastSeen(); !t.IsZero() {
			m.LastSeen = types.StringValue(t.UTC().Format(time.RFC3339))
		}
		if dev := ci.device; dev != nil {
			m.Hostname, m.Vendor, m.Note = types.StringValue(dev.Hostname), types.StringValue(dev.OUI), types.StringValue(dev.Note)
			m.Reserved, m.NetworkID = types.BoolValue(dev.UseFixedIP), types.StringValue(dev.NetworkID)
			if dev.LocalDNSRecordEnabled {
				m.DNSName = types.StringValue(dev.LocalDNSRecord)
			}
		}
		if s := ci.status; s != nil {
			wired := s.IsWired || s.Type == "WIRED"
			conn := "wifi"
			if wired {
				conn = "wired"
			} else {
				m.SSID, m.Band = types.StringValue(s.ESSID), types.StringValue(band(s.Radio))
			}
			m.Status, m.Connection, m.Uplink = types.StringValue(s.Status), types.StringValue(conn), types.StringValue(s.UplinkName)
			if s.Hostname != "" {
				m.Hostname = types.StringValue(s.Hostname)
			}
			if s.OUI != "" {
				m.Vendor = types.StringValue(s.OUI)
			}
			if s.NetworkID != "" {
				m.NetworkID = types.StringValue(s.NetworkID)
			}
			if s.Status == "online" {
				m.Signal, m.Uptime = types.Int64Value(int64(s.Signal)), types.Int64Value(int64(s.Uptime))
			}
			m.Port, m.LinkMbps = types.Int64Value(int64(s.SwitchPort)), types.Int64Value(int64(s.WiredRateMbps))
			m.DownloadBytes, m.UploadBytes = types.Int64Value(int64(s.TxBytes)), types.Int64Value(int64(s.RxBytes))
		}
		switch {
		case !cfg.Status.IsNull() && m.Status.ValueString() != cfg.Status.ValueString(),
			!cfg.Connection.IsNull() && m.Connection.ValueString() != cfg.Connection.ValueString(),
			!cfg.Blocked.IsNull() && ci.blocked() != cfg.Blocked.ValueBool(),
			networkID != "" && m.NetworkID.ValueString() != networkID:
			continue
		}
		cfg.Clients = append(cfg.Clients, m)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
