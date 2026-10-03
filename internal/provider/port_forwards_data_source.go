package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = &portForwardsDataSource{}

type portForwardsDataSource struct {
	data *providerData
}

type portForwardsModel struct {
	PortForwards []portForwardModel `tfsdk:"port_forwards"`
}

func newPortForwardsDataSource() datasource.DataSource { return &portForwardsDataSource{} }

func (d *portForwardsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_port_forwards"
}

func (d *portForwardsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the router's port forwarding rules, including those made in the web UI.",
		Attributes: map[string]schema.Attribute{
			"port_forwards": schema.ListNestedAttribute{
				Computed:    true,
				Description: "All port forwarding rules, sorted by name.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":           str("The rule's ID."),
					"name":         str("The rule's name."),
					"port":         str("Port, range or list on the router's internet side."),
					"forward_ip":   str("LAN address it forwards to."),
					"forward_port": str("Port on forward_ip."),
					"protocol":     str(`"tcp", "udp" or "tcp_udp".`),
					"source":       str(`Who may connect: "any", an address or a network.`),
					"wan":          str(`"wan", "wan2" or "both".`),
					"enabled":      schema.BoolAttribute{Computed: true, Description: "Whether the rule is active."},
					"log":          schema.BoolAttribute{Computed: true, Description: "Whether connections are logged."},
				}},
			},
		},
	}
}

func (d *portForwardsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *portForwardsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	list, err := d.data.client.ListPortForwards(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing port forwards", err.Error())
		return
	}
	sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	m := portForwardsModel{PortForwards: []portForwardModel{}}
	for _, pf := range list {
		e := forwardState(pf, types.StringValue(""))
		if pf.ForwardPort == "" {
			e.ForwardPort = types.StringValue(pf.Port)
		}
		if pf.Source == "" {
			e.Source = types.StringValue("any")
		}
		m.PortForwards = append(m.PortForwards, e)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
