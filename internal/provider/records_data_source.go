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
)

var _ datasource.DataSourceWithConfigure = &recordsDataSource{}

type recordsDataSource struct {
	data *providerData
}

type recordsModel struct {
	Type    types.String  `tfsdk:"type"`
	Name    types.String  `tfsdk:"name"`
	Records []recordModel `tfsdk:"records"`
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
		Description: "Lists the static DNS records on the router, including ones not managed by Terraform.",
		Attributes: map[string]schema.Attribute{
			"type": schema.StringAttribute{
				Optional:    true,
				Description: "Only return records of this type.",
				Validators:  []validator.String{stringvalidator.OneOf(recordTypes...)},
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Only return records with exactly this name.",
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
	records, err := d.data.client.List(ctx)
	d.data.warnIfLoginWaited(&resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Error listing DNS records", err.Error())
		return
	}
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.RecordType != b.RecordType {
			return a.RecordType < b.RecordType
		}
		return a.Value < b.Value
	})
	cfg.Records = []recordModel{}
	for _, r := range records {
		if !cfg.Type.IsNull() && !strings.EqualFold(r.RecordType, cfg.Type.ValueString()) {
			continue
		}
		if !cfg.Name.IsNull() && r.Key != cfg.Name.ValueString() {
			continue
		}
		cfg.Records = append(cfg.Records, fromRecord(r))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
