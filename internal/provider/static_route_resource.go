package provider

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &staticRouteResource{}
	_ resource.ResourceWithImportState    = &staticRouteResource{}
	_ resource.ResourceWithValidateConfig = &staticRouteResource{}
	_ datasource.DataSourceWithConfigure  = &staticRoutesDataSource{}
)

type staticRouteResource struct{ data *providerData }

type staticRouteModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Network  types.String `tfsdk:"network"`
	NextHop  types.String `tfsdk:"next_hop"`
	Distance types.Int64  `tfsdk:"distance"`
	Enabled  types.Bool   `tfsdk:"enabled"`
}

func newStaticRouteResource() resource.Resource { return &staticRouteResource{} }

func (r *staticRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_static_route"
}

func (r *staticRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A static route (Settings → Routing → Static Routes): traffic for network goes to next_hop, e.g. " +
			"another router on your LAN with its own network behind it. The provider checks what the router doesn't: " +
			"the next hop is a device on one of your networks (not the router itself) and the name is unique.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The route's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the route, unique among static routes (case-insensitive).",
			},
			"network": schema.StringAttribute{
				Required:    true,
				Description: `Destination network in network form, e.g. "192.168.2.0/24". Not one of the router's own networks, and not 0.0.0.0/0.`,
			},
			"next_hop": schema.StringAttribute{
				Required:    true,
				Description: "Address of the router to send the traffic to; a device on one of your networks.",
				Validators:  []validator.String{ipv4Validator{}},
			},
			"distance": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Description: "Administrative distance, 1 to 255 (lower wins). Defaults to 1.",
				Validators:  []validator.Int64{int64validator.Between(1, 255)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the route is active. Defaults to true.",
			},
		},
	}
}

func (r *staticRouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.data = data
}

// ValidateConfig requires the network in network form, so the state always
// matches the configuration.
func (r *staticRouteResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg staticRouteModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if n := knownString(cfg.Network); n != nil {
		p, err := netip.ParsePrefix(*n)
		switch {
		case err != nil || !p.Addr().Is4():
			resp.Diagnostics.AddAttributeError(path.Root("network"), "Invalid network", fmt.Sprintf("%q must be a network such as 192.168.2.0/24", *n))
		case p.Bits() == 0:
			resp.Diagnostics.AddAttributeError(path.Root("network"), "Invalid network", "a default route (0.0.0.0/0) can't be a static route")
		case p != p.Masked():
			resp.Diagnostics.AddAttributeError(path.Root("network"), "Invalid network", fmt.Sprintf("write the network as %q", p.Masked().String()))
		}
	}
}

func (m staticRouteModel) toAPI() unifi.StaticRoute {
	return unifi.StaticRoute{ID: m.ID.ValueString(), Name: m.Name.ValueString(), Network: m.Network.ValueString(),
		NextHop: m.NextHop.ValueString(), Distance: int(m.Distance.ValueInt64()), Enabled: m.Enabled.ValueBool()}
}

func routeState(rt unifi.StaticRoute) staticRouteModel {
	return staticRouteModel{ID: types.StringValue(rt.ID), Name: types.StringValue(rt.Name), Network: types.StringValue(rt.Network),
		NextHop: types.StringValue(rt.NextHop), Distance: types.Int64Value(int64(rt.Distance)), Enabled: types.BoolValue(rt.Enabled)}
}

// apply checks the route against the router and creates or updates it.
func (r *staticRouteResource) apply(ctx context.Context, plan staticRouteModel) (staticRouteModel, error) {
	rt := plan.toAPI()
	networks, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		return staticRouteModel{}, err
	}
	routes, err := r.data.client.ListRoutes(ctx)
	if err != nil {
		return staticRouteModel{}, err
	}
	if err := check.StaticRoute(&rt, networks, routes); err != nil {
		return staticRouteModel{}, err
	}
	if rt.ID == "" {
		rt, err = r.data.client.CreateRoute(ctx, rt)
	} else {
		rt, err = r.data.client.UpdateRoute(ctx, rt.ID, map[string]any{"name": rt.Name, "enabled": rt.Enabled,
			"static-route_network": rt.Network, "static-route_nexthop": rt.NextHop, "static-route_distance": rt.Distance})
	}
	if err != nil {
		return staticRouteModel{}, err
	}
	return routeState(rt), nil
}

func (r *staticRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan staticRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue("")
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error creating static route", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *staticRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state staticRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	routes, err := r.data.client.ListRoutes(ctx)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading static routes", err.Error())
		return
	}
	for _, rt := range routes {
		if rt.ID == state.ID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, routeState(rt))...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // deleted outside Terraform
}

func (r *staticRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state staticRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	newState, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating static route", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *staticRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state staticRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.client.DeleteRoute(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil && !unifi.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting static route", err.Error())
	}
}

// ImportState accepts the route's network (e.g. 192.168.2.0/24), name or ID.
func (r *staticRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	routes, err := r.data.client.ListRoutes(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading static routes", err.Error())
		return
	}
	for _, rt := range routes {
		if rt.ID == req.ID || rt.Network == req.ID || strings.EqualFold(rt.Name, req.ID) {
			resp.Diagnostics.Append(resp.State.Set(ctx, routeState(rt))...)
			return
		}
	}
	resp.Diagnostics.AddError("Static route not found", fmt.Sprintf("No static route has the network, name or ID %q.", req.ID))
}

// staticRoutesDataSource lists the static routes.
type staticRoutesDataSource struct{ data *providerData }

type staticRoutesModel struct {
	Routes []staticRouteModel `tfsdk:"routes"`
}

func newStaticRoutesDataSource() datasource.DataSource { return &staticRoutesDataSource{} }

func (d *staticRoutesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_static_routes"
}

func (d *staticRoutesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = dschema.Schema{
		Description: "Lists the router's static routes, including those made in the web UI.",
		Attributes: map[string]dschema.Attribute{
			"routes": dschema.ListNestedAttribute{
				Computed:    true,
				Description: "The static routes, sorted by network.",
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"id":       str("The route's ID."),
					"name":     str("The route's name."),
					"network":  str(`Destination network, e.g. "192.168.2.0/24".`),
					"next_hop": str("Address the traffic goes to."),
					"distance": dschema.Int64Attribute{Computed: true, Description: "Administrative distance."},
					"enabled":  dschema.BoolAttribute{Computed: true, Description: "Whether the route is active."},
				}},
			},
		},
	}
}

func (d *staticRoutesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = configureDS(req, resp)
}

func (d *staticRoutesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	routes, err := d.data.client.ListRoutes(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing static routes", err.Error())
		return
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Network < routes[j].Network })
	m := staticRoutesModel{Routes: []staticRouteModel{}}
	for _, rt := range routes {
		m.Routes = append(m.Routes, routeState(rt))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
