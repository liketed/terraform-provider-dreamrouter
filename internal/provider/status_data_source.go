package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &statusDataSource{}

type statusDataSource struct {
	data *providerData
}

type statusDeviceModel struct {
	Name       types.String `tfsdk:"name"`
	Model      types.String `tfsdk:"model"`
	Type       types.String `tfsdk:"type"`
	MAC        types.String `tfsdk:"mac"`
	IP         types.String `tfsdk:"ip"`
	Version    types.String `tfsdk:"version"`
	Online     types.Bool   `tfsdk:"online"`
	Upgradable types.Bool   `tfsdk:"upgradable"`
	UpgradeTo  types.String `tfsdk:"upgrade_to"`
}

type statusModel struct {
	Name            types.String        `tfsdk:"name"`
	Model           types.String        `tfsdk:"model"`
	OSVersion       types.String        `tfsdk:"os_version"`
	NetworkVersion  types.String        `tfsdk:"network_version"`
	Timezone        types.String        `tfsdk:"timezone"`
	Uptime          types.Int64         `tfsdk:"uptime"`
	InternetStatus  types.String        `tfsdk:"internet_status"`
	WANUp           types.Bool          `tfsdk:"wan_up"`
	WANIP           types.String        `tfsdk:"wan_ip"`
	ISP             types.String        `tfsdk:"isp"`
	ASN             types.Int64         `tfsdk:"asn"`
	WANInterface    types.String        `tfsdk:"wan_interface"`
	WANLinkMbps     types.Int64         `tfsdk:"wan_link_mbps"`
	PPPoE           types.Bool          `tfsdk:"pppoe"`
	LatencyMs       types.Int64         `tfsdk:"latency_ms"`
	CPUPercent      types.Float64       `tfsdk:"cpu_percent"`
	MemoryPercent   types.Float64       `tfsdk:"memory_percent"`
	CPUTemperature  types.Float64       `tfsdk:"cpu_temperature"`
	Clients         types.Int64         `tfsdk:"clients"`
	WiredClients    types.Int64         `tfsdk:"wired_clients"`
	WiFiClients     types.Int64         `tfsdk:"wifi_clients"`
	AccessPoints    types.Int64         `tfsdk:"access_points"`
	Switches        types.Int64         `tfsdk:"switches"`
	UpdateAvailable types.Bool          `tfsdk:"update_available"`
	Devices         []statusDeviceModel `tfsdk:"devices"`
	SpeedTestRun    types.String        `tfsdk:"speedtest_run"`
	SpeedTestDown   types.Float64       `tfsdk:"speedtest_download_mbps"`
	SpeedTestUp     types.Float64       `tfsdk:"speedtest_upload_mbps"`
}

func newStatusDataSource() datasource.DataSource { return &statusDataSource{} }

func (d *statusDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_status"
}

func (d *statusDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	num := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: desc}
	}
	flt := func(desc string) schema.Float64Attribute {
		return schema.Float64Attribute{Computed: true, Description: desc}
	}
	boo := func(desc string) schema.BoolAttribute { return schema.BoolAttribute{Computed: true, Description: desc} }
	resp.Schema = schema.Schema{
		Description: "An overview of the router: versions, internet connection, load, client counts, firmware and the " +
			"last speed test. Most values change all the time; the stable ones, such as the WAN IP address and the " +
			"versions, are the useful inputs elsewhere, e.g. the WAN IP in a DNS record at another provider.",
		Attributes: map[string]schema.Attribute{
			"name":             str(`The router's name, e.g. "Dream Router 7".`),
			"model":            str(`The router's model code, e.g. "UDMA67A".`),
			"os_version":       str(`UniFi OS version, e.g. "5.1.33".`),
			"network_version":  str(`UniFi Network application version, e.g. "10.6.106".`),
			"timezone":         str(`The router's time zone, e.g. "Europe/Dublin".`),
			"uptime":           num("Seconds since the router started."),
			"internet_status":  str(`"ok" when the internet is reachable; otherwise e.g. "warning" or "error".`),
			"wan_up":           boo("Whether the (first) WAN link is up."),
			"wan_ip":           str("The public (WAN) IP address."),
			"isp":              str("The internet provider's name."),
			"asn":              num("The internet provider's autonomous system number."),
			"wan_interface":    str(`The WAN port, e.g. "eth3".`),
			"wan_link_mbps":    num("The WAN port's link speed in Mbit/s."),
			"pppoe":            boo("Whether the internet connection uses PPPoE."),
			"latency_ms":       num("Latency to the internet in milliseconds."),
			"cpu_percent":      flt("The router's CPU use in percent."),
			"memory_percent":   flt("The router's memory use in percent."),
			"cpu_temperature":  flt("The router's CPU temperature in °C; 0 if not reported."),
			"clients":          num("Clients connected now."),
			"wired_clients":    num("Wired clients connected now."),
			"wifi_clients":     num("Wi-Fi clients connected now."),
			"access_points":    num("Access points, including the router's built-in Wi-Fi."),
			"switches":         num("Switches, including the router's built-in switch."),
			"update_available": boo("Whether the Network application or any UniFi device has an update available."),
			"devices": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The UniFi devices: the router itself, access points and switches.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":       str("Device name."),
					"model":      str("Model code."),
					"type":       str(`"udm" (router), "uap" (access point), "usw" (switch), ...`),
					"mac":        str("MAC address."),
					"ip":         str("IP address."),
					"version":    str("Firmware version."),
					"online":     boo("Whether the device is connected."),
					"upgradable": boo("Whether a firmware update is available."),
					"upgrade_to": str("The available firmware version; empty if none."),
				}},
			},
			"speedtest_run":           str("When the last speed test ran, as an RFC 3339 timestamp (UTC); empty if never."),
			"speedtest_download_mbps": flt("Download speed in the last speed test, in Mbit/s; 0 if never run."),
			"speedtest_upload_mbps":   flt("Upload speed in the last speed test, in Mbit/s; 0 if never run."),
		},
	}
}

func (d *statusDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *statusDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	s, err := d.data.client.GetStatus(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading router status", err.Error())
		return
	}
	in, sy := s.Internet, s.System
	m := statusModel{
		Name: types.StringValue(s.Name), Model: types.StringValue(s.Model), OSVersion: types.StringValue(s.OSVersion),
		NetworkVersion: types.StringValue(s.NetworkVersion), Timezone: types.StringValue(s.Timezone),
		Uptime:         types.Int64Value(int64(s.Uptime / time.Second)),
		InternetStatus: types.StringValue(in.Status), WANUp: types.BoolValue(in.Up), WANIP: types.StringValue(in.IP),
		ISP: types.StringValue(in.ISP), ASN: types.Int64Value(int64(in.ASN)), WANInterface: types.StringValue(in.Interface),
		WANLinkMbps: types.Int64Value(int64(in.LinkMbps)), PPPoE: types.BoolValue(in.PPPoE), LatencyMs: types.Int64Value(int64(in.LatencyMs)),
		CPUPercent: types.Float64Value(sy.CPUPercent), MemoryPercent: types.Float64Value(sy.MemoryPercent),
		CPUTemperature: types.Float64Value(sy.CPUTempC),
		Clients:        types.Int64Value(int64(s.Clients)), WiredClients: types.Int64Value(int64(s.Wired)), WiFiClients: types.Int64Value(int64(s.WiFi)),
		AccessPoints: types.Int64Value(int64(s.AccessPoints)), Switches: types.Int64Value(int64(s.Switches)),
		UpdateAvailable: types.BoolValue(s.UpdateAvailable), Devices: []statusDeviceModel{},
		SpeedTestRun:  types.StringValue(""),
		SpeedTestDown: types.Float64Value(s.SpeedTest.DownloadMbps), SpeedTestUp: types.Float64Value(s.SpeedTest.UploadMbps),
	}
	if !s.SpeedTest.Run.IsZero() {
		m.SpeedTestRun = types.StringValue(s.SpeedTest.Run.UTC().Format(time.RFC3339))
	}
	for _, dev := range s.Devices {
		m.Devices = append(m.Devices, statusDeviceModel{
			Name: types.StringValue(dev.Name), Model: types.StringValue(dev.Model), Type: types.StringValue(dev.Type),
			MAC: types.StringValue(dev.MAC), IP: types.StringValue(dev.IP), Version: types.StringValue(dev.Version),
			Online: types.BoolValue(dev.Online), Upgradable: types.BoolValue(dev.Upgradable), UpgradeTo: types.StringValue(dev.UpgradeTo),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
