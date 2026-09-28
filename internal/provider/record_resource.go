package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"

	"github.com/liketed/dreamrouter-go/check"
	"github.com/liketed/dreamrouter-go/unifi"
)

var (
	_ resource.ResourceWithConfigure      = &recordResource{}
	_ resource.ResourceWithImportState    = &recordResource{}
	_ resource.ResourceWithValidateConfig = &recordResource{}
)

type recordResource struct {
	data *providerData
}

type recordModel struct {
	ID       types.String `tfsdk:"id"`
	Type     types.String `tfsdk:"type"`
	Name     types.String `tfsdk:"name"`
	Value    types.String `tfsdk:"value"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Priority types.Int64  `tfsdk:"priority"`
	Weight   types.Int64  `tfsdk:"weight"`
	Port     types.Int64  `tfsdk:"port"`
	Enabled  types.Bool   `tfsdk:"enabled"`
}

func newRecordResource() resource.Resource { return &recordResource{} }

func (r *recordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *recordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	u16 := []validator.Int64{int64validator.Between(0, 65535)}
	resp.Schema = schema.Schema{
		Description: "A static DNS record on the router (Settings → Routing → DNS). " +
			"Supported types: " + strings.Join(check.RecordTypes, ", ") + ".",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The record's ID on the router.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"type": schema.StringAttribute{
				Required:      true,
				Description:   "Record type: " + strings.Join(check.RecordTypes, ", ") + ". Changing it replaces the record.",
				Validators:    []validator.String{stringvalidator.OneOf(check.RecordTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "The record's name, e.g. \"nas.home.internal\". For SRV records, " +
					"\"_service._protocol.domain\", e.g. \"_sip._tcp.home.internal\". Changing it replaces the record.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				Required: true,
				Description: "A: IPv4 address. AAAA: IPv6 address. CNAME: target hostname. MX: mail server. " +
					"SRV: target server. TXT: text (double quotes only around the whole value, max 255 " +
					"characters per line). NS: IP address of the DNS server to forward the name to " +
					"(the router implements NS records as conditional forwarders).",
			},
			"ttl": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				Description: "TTL in seconds for A, AAAA and CNAME records. 0 (the default) means automatic.",
				Validators:  []validator.Int64{int64validator.Between(0, 2147483647)},
			},
			"priority": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				Description: "Priority for MX and SRV records.", Validators: u16,
			},
			"weight": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				Description: "Weight for SRV records.", Validators: u16,
			},
			"port": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
				Description: "Port for SRV records.", Validators: u16,
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the record is served. Defaults to true.",
			},
		},
	}
}

func (r *recordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *recordResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg recordModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	problems := check.DNSProblems(check.DNSInput{
		Type: knownString(cfg.Type), Name: knownString(cfg.Name), Value: knownString(cfg.Value),
		TTL: knownInt(cfg.TTL), Priority: knownInt(cfg.Priority), Weight: knownInt(cfg.Weight), Port: knownInt(cfg.Port),
	})
	delete(problems, "type") // the schema's OneOf validator reports unknown types
	attrs := make([]string, 0, len(problems))
	for a := range problems {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)
	for _, a := range attrs {
		resp.Diagnostics.AddAttributeError(path.Root(a), "Invalid DNS record", problems[a])
	}
}

func (r *recordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan recordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	created, err := r.data.client.CreateDNS(ctx, toRecord(plan))
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		detail := err.Error()
		if unifi.HasCode(err, unifi.CodeDNSRecordAlreadyExists) {
			detail += fmt.Sprintf("\n\nA %s record %q with this value already exists on the router. "+
				"If another Terraform project manages it, remove it from this configuration instead: "+
				"a record should be managed by only one project, because destroying either would delete it. "+
				"If nothing else manages it, import it:\n  terraform import <address> %s/%s/%s",
				plan.Type.ValueString(), plan.Name.ValueString(),
				plan.Type.ValueString(), plan.Name.ValueString(), plan.Value.ValueString())
		}
		if unifi.HasCode(err, unifi.CodeDNSOverlapsWithDeviceName) {
			detail += fmt.Sprintf("\n\n%q is already a device's DNS name (a dreamrouter_host, drctl host, or the "+
				"device's local DNS record in the web UI). Remove it there first, or choose another name.", plan.Name.ValueString())
		}
		resp.Diagnostics.AddError("Error creating DNS record", detail)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromRecord(created))...)
}

func (r *recordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state recordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rec, err := r.data.client.GetDNS(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if unifi.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform; plan will recreate it
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading DNS record", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromRecord(rec))...)
}

func (r *recordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state recordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rec := toRecord(plan)
	rec.ID = state.ID.ValueString()
	updated, err := r.data.client.UpdateDNS(ctx, rec)
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error updating DNS record", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, fromRecord(updated))...)
}

func (r *recordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state recordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.client.DeleteDNS(ctx, state.ID.ValueString())
	r.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil && !unifi.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting DNS record", err.Error())
	}
}

var routerID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// ImportState accepts either the router's record ID, "TYPE/name", or
// "TYPE/name/value" when several records share a type and name.
func (r *recordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !routerID.MatchString(id) {
		parts := strings.SplitN(id, "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			resp.Diagnostics.AddError("Invalid import ID",
				`Use "TYPE/name" (e.g. "A/nas.home.internal"), "TYPE/name/value", or the router's record ID.`)
			return
		}
		records, err := r.data.client.ListDNS(ctx)
		r.data.warnIfLoginWaited(&resp.Diagnostics)
		if err != nil {
			resp.Diagnostics.AddError("Error listing DNS records", err.Error())
			return
		}
		var matches []unifi.DNSRecord
		for _, rec := range records {
			if strings.EqualFold(rec.RecordType, parts[0]) && rec.Key == parts[1] &&
				(len(parts) == 2 || rec.Value == parts[2]) {
				matches = append(matches, rec)
			}
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddError("DNS record not found", fmt.Sprintf("No record matches %q.", id))
			return
		case 1:
			id = matches[0].ID
		default:
			values := make([]string, len(matches))
			for i, m := range matches {
				values[i] = fmt.Sprintf("  %s/%s/%s", m.RecordType, m.Key, m.Value)
			}
			resp.Diagnostics.AddError("Several DNS records match",
				fmt.Sprintf("%q matches %d records. Add the value to pick one:\n%s", id, len(matches),
					strings.Join(values, "\n")))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

func toRecord(m recordModel) unifi.DNSRecord {
	return unifi.DNSRecord{
		ID:         m.ID.ValueString(),
		RecordType: m.Type.ValueString(),
		Key:        m.Name.ValueString(),
		Value:      m.Value.ValueString(),
		Enabled:    m.Enabled.ValueBool(),
		TTL:        m.TTL.ValueInt64(),
		Priority:   m.Priority.ValueInt64(),
		Weight:     m.Weight.ValueInt64(),
		Port:       m.Port.ValueInt64(),
	}
}

func fromRecord(r unifi.DNSRecord) recordModel {
	return recordModel{
		ID:       types.StringValue(r.ID),
		Type:     types.StringValue(r.RecordType),
		Name:     types.StringValue(r.Key),
		Value:    types.StringValue(r.Value),
		TTL:      types.Int64Value(r.TTL),
		Priority: types.Int64Value(r.Priority),
		Weight:   types.Int64Value(r.Weight),
		Port:     types.Int64Value(r.Port),
		Enabled:  types.BoolValue(r.Enabled),
	}
}

func knownString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func knownInt(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}
