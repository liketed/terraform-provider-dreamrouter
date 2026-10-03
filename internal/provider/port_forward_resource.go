package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &portForwardResource{}
	_ resource.ResourceWithImportState    = &portForwardResource{}
	_ resource.ResourceWithValidateConfig = &portForwardResource{}
)

type portForwardResource struct {
	data *providerData
}

type portForwardModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Port        types.String `tfsdk:"port"`
	ForwardIP   types.String `tfsdk:"forward_ip"`
	ForwardPort types.String `tfsdk:"forward_port"`
	Protocol    types.String `tfsdk:"protocol"`
	Source      types.String `tfsdk:"source"`
	WAN         types.String `tfsdk:"wan"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Log         types.Bool   `tfsdk:"log"`
}

func newPortForwardResource() resource.Resource { return &portForwardResource{} }

func (r *portForwardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_port_forward"
}

func (r *portForwardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A port forwarding rule (Settings → Routing → Port Forwarding): connections from the internet to " +
			"`port` on the router are forwarded to `forward_ip`. An enabled rule opens that port to the internet (or " +
			"to `source`). The provider checks what the router doesn't: the address is on one of your LANs and not the " +
			"router, ranges are ascending, the name is unique, and no other enabled rule forwards the same port.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The rule's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the rule, unique among port forwards (case-insensitive).",
				Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"port": schema.StringAttribute{
				Required: true,
				Description: `Port on the router's internet side: a port ("8443"), an ascending range ("40010-40020") ` +
					`or a list ("80,443"), without spaces.`,
			},
			"forward_ip": schema.StringAttribute{
				Required:    true,
				Description: "LAN address to forward to. It must be a host on one of the router's networks, not the router itself.",
				Validators:  []validator.String{ipv4Validator{}},
			},
			"forward_port": schema.StringAttribute{
				Optional: true,
				Description: "Port on `forward_ip`. Defaults to the same as `port`. Only a single port can be forwarded to a " +
					"different one; a range or list is always forwarded to the same ports.",
			},
			"protocol": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("tcp_udp"),
				Description: `"tcp", "udp" or "tcp_udp" (both). Defaults to "tcp_udp".`,
				Validators:  []validator.String{stringvalidator.OneOf("tcp", "udp", "tcp_udp")},
			},
			"source": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("any"),
				Description: `Who may connect: "any" (the default), an IPv4 address, or a network such as "203.0.113.0/24".`,
			},
			"wan": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("wan"),
				Description: `Which internet connection: "wan" (the default), or "wan2" or "both" on a router with two.`,
				Validators:  []validator.String{stringvalidator.OneOf("wan", "wan2", "both")},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the rule is active. Defaults to true. A disabled rule opens nothing.",
			},
			"log": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether to log connections that use the rule. Defaults to false.",
			},
		},
	}
}

func (r *portForwardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig checks what can be checked without the router, requiring
// the router's form of each value so the state always matches the config.
func (r *portForwardResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg portForwardModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for attr, v := range map[string]types.String{"port": cfg.Port, "forward_port": cfg.ForwardPort} {
		if s := knownString(v); s != nil {
			if norm, err := check.Ports(*s); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root(attr), "Invalid port", err.Error())
			} else if norm != *s {
				resp.Diagnostics.AddAttributeError(path.Root(attr), "Invalid port", fmt.Sprintf("write %q without spaces: %q", *s, norm))
			}
		}
	}
	if s := knownString(cfg.Source); s != nil {
		if norm, err := check.Source(*s); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("source"), "Invalid source", err.Error())
		} else if norm != *s {
			resp.Diagnostics.AddAttributeError(path.Root("source"), "Invalid source", fmt.Sprintf("write the source as %q, the form the router uses", norm))
		}
	}
	port, fwd := knownString(cfg.Port), knownString(cfg.ForwardPort)
	if port != nil && fwd != nil && *fwd != *port && (strings.ContainsAny(*port, ",-") || strings.ContainsAny(*fwd, ",-")) {
		resp.Diagnostics.AddAttributeError(path.Root("forward_port"), "Invalid forward port",
			fmt.Sprintf("a range or list of ports can only be forwarded to the same ports (%s), not %s; leave forward_port out", *port, *fwd))
	}
}

func (m portForwardModel) toAPI() unifi.PortForward {
	pf := unifi.PortForward{
		ID: m.ID.ValueString(), Name: m.Name.ValueString(), Enabled: m.Enabled.ValueBool(), Interface: m.WAN.ValueString(),
		Source: m.Source.ValueString(), Port: m.Port.ValueString(), ForwardIP: m.ForwardIP.ValueString(),
		ForwardPort: m.ForwardPort.ValueString(), Protocol: m.Protocol.ValueString(), Log: m.Log.ValueBool(),
	}
	return pf // check.PortForward fills in defaults, e.g. ForwardPort = Port
}

// forwardState describes a rule; forward_port stays null if it was null and
// the rule forwards to the same port.
func forwardState(pf unifi.PortForward, prevForwardPort types.String) portForwardModel {
	m := portForwardModel{
		ID: types.StringValue(pf.ID), Name: types.StringValue(pf.Name), Port: types.StringValue(pf.Port),
		ForwardIP: types.StringValue(pf.ForwardIP), Protocol: types.StringValue(pf.Protocol), Source: types.StringValue(pf.Source),
		WAN: types.StringValue(pf.Interface), Enabled: types.BoolValue(pf.Enabled), Log: types.BoolValue(pf.Log),
		ForwardPort: types.StringValue(pf.ForwardPort),
	}
	if pf.ForwardPort == "" || (prevForwardPort.IsNull() && pf.ForwardPort == pf.Port) {
		m.ForwardPort = types.StringNull()
	}
	return m
}

// apply validates the rule against the router and creates or updates it.
func (r *portForwardResource) apply(ctx context.Context, plan portForwardModel) (portForwardModel, error) {
	pf := plan.toAPI()
	networks, err := r.data.client.ListNetworks(ctx)
	if err != nil {
		return portForwardModel{}, err
	}
	existing, err := r.data.client.ListPortForwards(ctx)
	if err != nil {
		return portForwardModel{}, err
	}
	if err := check.PortForward(&pf, networks, existing); err != nil {
		return portForwardModel{}, err
	}
	if pf.ID == "" {
		pf, err = r.data.client.CreatePortForward(ctx, pf)
	} else {
		pf, err = r.data.client.UpdatePortForward(ctx, pf.ID, pf)
	}
	if err != nil {
		return portForwardModel{}, err
	}
	return forwardState(pf, plan.ForwardPort), nil
}

func (r *portForwardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan portForwardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue("")
	state, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error creating port forward", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *portForwardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state portForwardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := r.data.client.ListPortForwards(ctx)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error reading port forwards", err.Error())
		return
	}
	for _, pf := range list {
		if pf.ID == state.ID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, forwardState(pf, state.ForwardPort))...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // deleted outside Terraform
}

func (r *portForwardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state portForwardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	newState, err := r.apply(ctx, plan)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating port forward", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *portForwardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state portForwardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.client.DeletePortForward(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil && !unifi.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting port forward", err.Error())
	}
}

// ImportState accepts the rule's name (case-insensitive) or ID.
func (r *portForwardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	list, err := r.data.client.ListPortForwards(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading port forwards", err.Error())
		return
	}
	for _, pf := range list {
		if pf.ID == req.ID || strings.EqualFold(pf.Name, req.ID) {
			resp.Diagnostics.Append(resp.State.Set(ctx, forwardState(pf, types.StringNull()))...)
			return
		}
	}
	resp.Diagnostics.AddError("Port forward not found", fmt.Sprintf("No port forward is named %q or has that ID.", req.ID))
}
