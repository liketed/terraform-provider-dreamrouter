package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &wansDataSource{}
	_ datasource.DataSourceWithConfigure = &portsDataSource{}
)

type wansDataSource struct{ data *providerData }
type portsDataSource struct{ data *providerData }

type wanEntry struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	NetworkGroup types.String `tfsdk:"network_group"`
	Port         types.String `tfsdk:"port"`
	Type         types.String `tfsdk:"type"`
	Username     types.String `tfsdk:"username"`
	VLAN         types.Int64  `tfsdk:"vlan"`
	Up           types.Bool   `tfsdk:"up"`
	IP           types.String `tfsdk:"ip"`
	Role         types.String `tfsdk:"role"`
}

type portEntry struct {
	Index     types.Int64  `tfsdk:"index"`
	Name      types.String `tfsdk:"name"`
	Interface types.String `tfsdk:"interface"`
	Media     types.String `tfsdk:"media"`
	Up        types.Bool   `tfsdk:"up"`
	SpeedMbps types.Int64  `tfsdk:"speed_mbps"`
	Role      types.String `tfsdk:"role"`
}

func newWANsDataSource() datasource.DataSource  { return &wansDataSource{} }
func newPortsDataSource() datasource.DataSource { return &portsDataSource{} }

func (d *wansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wans"
}
func (d *portsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ports"
}

func (d *wansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the router's internet connections with the port each uses and its live state.",
		Attributes: map[string]schema.Attribute{
			"wans": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The connections, primary first.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":            str("The connection's ID."),
					"name":          str(`e.g. "Internet 1".`),
					"network_group": str(`"WAN" (Internet 1) or "WAN2" (Internet 2).`),
					"port":          str(`The port it uses, e.g. "Port 4"; empty if none.`),
					"type":          str(`"pppoe", "dhcp" or "static".`),
					"username":      str("PPPoE username; empty otherwise."),
					"vlan":          schema.Int64Attribute{Computed: true, Description: "VLAN ID; 0 if none."},
					"up":            schema.BoolAttribute{Computed: true, Description: "Whether the connection is up."},
					"ip":            str("Its public address; empty if down."),
					"role":          str(`"primary", "failover" or "load balanced".`),
				}},
			},
		},
	}
}

func (d *portsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the router's own ports and their roles.",
		Attributes: map[string]schema.Attribute{
			"ports": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The ports, in the web UI's order.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"index":      schema.Int64Attribute{Computed: true, Description: "Number in the web UI (1 = Port 1)."},
					"name":       str(`e.g. "Port 3", "SFP+ 1".`),
					"interface":  str(`e.g. "eth2".`),
					"media":      str(`e.g. "2.5GE", "SFP+".`),
					"up":         schema.BoolAttribute{Computed: true, Description: "Whether a cable (or module) has a link."},
					"speed_mbps": schema.Int64Attribute{Computed: true, Description: "Link speed; 0 if down."},
					"role":       str(`"LAN", "WAN" (Internet 1) or "WAN2" (Internet 2).`),
				}},
			},
		},
	}
}

func configureDS(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *providerData {
	if req.ProviderData == nil {
		return nil
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
	}
	return data
}

func (d *wansDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureDS(req, resp)
}
func (d *portsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureDS(req, resp)
}

func (d *wansDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	wans, err := d.data.client.ListWANs(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing internet connections", err.Error())
		return
	}
	ports, links, err := d.data.client.Ports(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading ports", err.Error())
		return
	}
	out := []wanEntry{}
	for _, w := range wans {
		e := wanEntry{ID: types.StringValue(w.ID), Name: types.StringValue(w.Name), NetworkGroup: types.StringValue(w.NetworkGroup),
			Port: types.StringValue(""), Type: types.StringValue(w.Type), Username: types.StringValue(""), VLAN: types.Int64Value(0),
			Up: types.BoolValue(false), IP: types.StringValue(""), Role: types.StringValue("load balanced")}
		if w.Type == "pppoe" {
			e.Username = types.StringValue(w.Username)
		}
		if w.VLANEnabled {
			e.VLAN = types.Int64Value(int64(w.VLAN))
		}
		for _, p := range ports {
			if p.Role == w.NetworkGroup {
				e.Port = types.StringValue(p.Name)
			}
		}
		for _, l := range links {
			if l.NetworkGroup == w.NetworkGroup {
				e.Up, e.IP = types.BoolValue(l.Up), types.StringValue(l.IP)
			}
		}
		switch {
		case w.FailoverPriority == 1 || (w.FailoverPriority == 0 && w.NetworkGroup == "WAN"):
			e.Role = types.StringValue("primary")
		case w.LoadBalanceType == "failover-only":
			e.Role = types.StringValue("failover")
		}
		out = append(out, e)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("wans"), out)...)
}

func (d *portsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ports, _, err := d.data.client.Ports(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading ports", err.Error())
		return
	}
	out := []portEntry{}
	for _, p := range ports {
		speed := int64(0)
		if p.Up {
			speed = int64(p.SpeedMbps)
		}
		out = append(out, portEntry{Index: types.Int64Value(int64(p.Index)), Name: types.StringValue(p.Name), Interface: types.StringValue(p.Interface),
			Media: types.StringValue(p.Media), Up: types.BoolValue(p.Up), SpeedMbps: types.Int64Value(speed), Role: types.StringValue(p.Role)})
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, pathRoot("ports"), out)...)
}

func pathRoot(name string) path.Path { return path.Root(name) }
