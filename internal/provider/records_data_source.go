package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/liketed/dreamrouter-go/check"
)

var _ datasource.DataSourceWithConfigure = &recordsDataSource{}

type recordsDataSource struct {
	data *providerData
}

type recordsModel struct {
	Type       types.String      `tfsdk:"type"`
	Name       types.String      `tfsdk:"name"`
	StaticOnly types.Bool        `tfsdk:"static_only"`
	Records    []listRecordModel `tfsdk:"records"`
}

// listRecordModel is a record in the data source: a static record, or a
// device's DNS name (source "host"), which the router serves like an A record.
type listRecordModel struct {
	ID       types.String `tfsdk:"id"`
	Type     types.String `tfsdk:"type"`
	Name     types.String `tfsdk:"name"`
	Value    types.String `tfsdk:"value"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Priority types.Int64  `tfsdk:"priority"`
	Weight   types.Int64  `tfsdk:"weight"`
	Port     types.Int64  `tfsdk:"port"`
	Enabled  types.Bool   `tfsdk:"enabled"`
	Source   types.String `tfsdk:"source"`
	MAC      types.String `tfsdk:"mac"`
}

func newRecordsDataSource() datasource.DataSource { return &recordsDataSource{} }

func (d *recordsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_records"
}

func (d *recordsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Lists the names the router answers for: static DNS records and devices' DNS names " +
			"(dreamrouter_host), including ones not managed by Terraform. It is read-only: listing a record " +
			"doesn't make the project manage it.",
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				Optional:    true,
				Description: "Only return records of this type.",
				Validators:  []validator.String{stringvalidator.OneOf(check.RecordTypes...)},
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Only return records with exactly this name.",
			},
			"static_only": schema.BoolAttribute{
				Optional:    true,
				Description: "Only return static DNS records, not devices' DNS names. Defaults to false.",
			},
			"records": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Matching records, sorted by name, type and value.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":       computed("The record's ID on the router."),
						"type":     computed("Record type."),
						"name":     computed("Record name."),
						"value":    computed("Record value."),
						"ttl":      schema.Int64Attribute{Computed: true, Description: "TTL in seconds; 0 means automatic."},
						"priority": schema.Int64Attribute{Computed: true, Description: "Priority (MX, SRV)."},
						"weight":   schema.Int64Attribute{Computed: true, Description: "Weight (SRV)."},
						"port":     schema.Int64Attribute{Computed: true, Description: "Port (SRV)."},
						"enabled":  schema.BoolAttribute{Computed: true, Description: "Whether the record is served."},
						"source": computed(`"static" for a static DNS record, or "host" for a device's DNS name ` +
							"(served like an A record; managed with dreamrouter_host)."),
						"mac": computed(`For source "host", the device's MAC address; otherwise empty.`),
					},
				},
			},
		},
	}
}

func (d *recordsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *recordsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg recordsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	records, err := d.data.client.ListDNS(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing DNS records", err.Error())
		return
	}
	entries := make([]listRecordModel, 0, len(records))
	for _, r := range records {
		m := fromRecord(r)
		entries = append(entries, listRecordModel{ID: m.ID, Type: m.Type, Name: m.Name, Value: m.Value, TTL: m.TTL,
			Priority: m.Priority, Weight: m.Weight, Port: m.Port, Enabled: m.Enabled,
			Source: types.StringValue("static"), MAC: types.StringValue("")})
	}
	if !cfg.StaticOnly.ValueBool() {
		clients, err := d.data.client.ListClients(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Error listing devices", err.Error())
			return
		}
		for _, c := range clients {
			if c.HasDNSName() {
				entries = append(entries, listRecordModel{ID: types.StringValue(c.ID), Type: types.StringValue("A"),
					Name: types.StringValue(c.LocalDNSRecord), Value: types.StringValue(c.FixedIP), TTL: types.Int64Value(0),
					Priority: types.Int64Value(0), Weight: types.Int64Value(0), Port: types.Int64Value(0),
					Enabled: types.BoolValue(true), Source: types.StringValue("host"), MAC: types.StringValue(c.MAC)})
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Name.ValueString() != b.Name.ValueString() {
			return a.Name.ValueString() < b.Name.ValueString()
		}
		if a.Type.ValueString() != b.Type.ValueString() {
			return a.Type.ValueString() < b.Type.ValueString()
		}
		return a.Value.ValueString() < b.Value.ValueString()
	})
	cfg.Records = []listRecordModel{}
	for _, e := range entries {
		if !cfg.Type.IsNull() && !strings.EqualFold(e.Type.ValueString(), cfg.Type.ValueString()) {
			continue
		}
		if !cfg.Name.IsNull() && e.Name.ValueString() != cfg.Name.ValueString() {
			continue
		}
		cfg.Records = append(cfg.Records, e)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
