package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/liketed/terraform-provider-dreamrouter/internal/fakerouter"
)

var providerFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"dreamrouter": providerserver.NewProtocol6WithError(New("test")()),
}

func fakeProviderConfig(r *fakerouter.Router) string {
	return fmt.Sprintf(`
provider "dreamrouter" {
  host     = %q
  username = %q
  password = %q
  insecure = true
}
`, r.Host(), fakerouter.Username, fakerouter.Password)
}

// findRecord returns the stored record with the given type and name.
func findRecord(r *fakerouter.Router, typ, name string) (fakerouter.Record, bool) {
	for _, rec := range r.Records() {
		if rec.RecordType == typ && rec.Key == name {
			return rec, true
		}
	}
	return fakerouter.Record{}, false
}

func checkStored(r *fakerouter.Router, typ, name string, check func(fakerouter.Record) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		rec, ok := findRecord(r, typ, name)
		if !ok {
			return fmt.Errorf("%s %s not stored on router", typ, name)
		}
		return check(rec)
	}
}

func checkEmpty(r *fakerouter.Router) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := len(r.Records()); n != 0 {
			return fmt.Errorf("%d records left on router after destroy", n)
		}
		return nil
	}
}

func TestRecordLifecycle(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := func(aValue string, srvPort int, cnameName string) string {
		return fakeProviderConfig(r) + fmt.Sprintf(`
resource "dreamrouter_dns_record" "a" {
  type  = "A"
  name  = "nas.home.internal"
  value = %q
}
resource "dreamrouter_dns_record" "aaaa" {
  type  = "AAAA"
  name  = "nas.home.internal"
  value = "fd00::50"
  ttl   = 300
}
resource "dreamrouter_dns_record" "cname" {
  type  = "CNAME"
  name  = %q
  value = "nas.home.internal"
}
resource "dreamrouter_dns_record" "mx" {
  type     = "MX"
  name     = "home.internal"
  value    = "mail.home.internal"
  priority = 10
}
resource "dreamrouter_dns_record" "ns" {
  type  = "NS"
  name  = "lab.home.internal"
  value = "192.168.1.2"
}
resource "dreamrouter_dns_record" "srv" {
  type     = "SRV"
  name     = "_sip._tcp.home.internal"
  value    = "pbx.home.internal"
  priority = 10
  weight   = 5
  port     = %d
}
resource "dreamrouter_dns_record" "txt" {
  type    = "TXT"
  name    = "home.internal"
  value   = "v=spf1 -all"
  enabled = false
}
`, aValue, cnameName, srvPort)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkEmpty(r),
		Steps: []resource.TestStep{
			{
				Config: cfg("192.168.1.50", 5060, "files.home.internal"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_dns_record.a", "value", "192.168.1.50"),
					resource.TestCheckResourceAttr("dreamrouter_dns_record.a", "ttl", "0"),
					resource.TestCheckResourceAttr("dreamrouter_dns_record.a", "enabled", "true"),
					resource.TestCheckResourceAttrSet("dreamrouter_dns_record.a", "id"),
					resource.TestCheckResourceAttr("dreamrouter_dns_record.txt", "enabled", "false"),
					checkStored(r, "SRV", "_sip._tcp.home.internal", func(rec fakerouter.Record) error {
						if rec.Port != 5060 || rec.Priority != 10 || rec.Weight != 5 || rec.Value != "pbx.home.internal" {
							return fmt.Errorf("SRV stored as %+v", rec)
						}
						return nil
					}),
					checkStored(r, "AAAA", "nas.home.internal", func(rec fakerouter.Record) error {
						if rec.TTL != 300 {
							return fmt.Errorf("AAAA ttl = %d", rec.TTL)
						}
						return nil
					}),
					func(*terraform.State) error {
						if n := len(r.Records()); n != 7 {
							return fmt.Errorf("%d records stored, want 7", n)
						}
						return nil
					},
				),
			},
			{
				// Value and port changes are updated in place; a name change replaces the record.
				Config: cfg("192.168.1.51", 5061, "share.home.internal"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("dreamrouter_dns_record.a", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("dreamrouter_dns_record.srv", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("dreamrouter_dns_record.cname", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction("dreamrouter_dns_record.mx", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_dns_record.a", "value", "192.168.1.51"),
					checkStored(r, "SRV", "_sip._tcp.home.internal", func(rec fakerouter.Record) error {
						if rec.Port != 5061 {
							return fmt.Errorf("SRV port = %d", rec.Port)
						}
						return nil
					}),
					func(*terraform.State) error {
						if _, ok := findRecord(r, "CNAME", "files.home.internal"); ok {
							return fmt.Errorf("old CNAME still stored")
						}
						if _, ok := findRecord(r, "CNAME", "share.home.internal"); !ok {
							return fmt.Errorf("new CNAME not stored")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "dreamrouter_dns_record.srv",
				ImportState:       true,
				ImportStateId:     "SRV/_sip._tcp.home.internal",
				ImportStateVerify: true,
			},
			{
				ResourceName:      "dreamrouter_dns_record.a",
				ImportState:       true,
				ImportStateId:     "A/nas.home.internal/192.168.1.51",
				ImportStateVerify: true,
			},
		},
	})
}

func TestRecordDrift(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cfg := fakeProviderConfig(r) + `
resource "dreamrouter_dns_record" "a" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.50"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             checkEmpty(r),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// Someone changes the IP in the web UI: Terraform puts it back in place.
				PreConfig: func() {
					rec, _ := findRecord(r, "A", "nas.home.internal")
					r.Modify(rec.ID, func(x *fakerouter.Record) { x.Value = "192.168.1.99" })
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("dreamrouter_dns_record.a", plancheck.ResourceActionUpdate),
					},
				},
				Check: checkStored(r, "A", "nas.home.internal", func(rec fakerouter.Record) error {
					if rec.Value != "192.168.1.50" {
						return fmt.Errorf("value = %s, want drift corrected", rec.Value)
					}
					return nil
				}),
			},
			{
				// Someone deletes it in the web UI: Terraform recreates it.
				PreConfig: func() {
					rec, _ := findRecord(r, "A", "nas.home.internal")
					r.Remove(rec.ID)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("dreamrouter_dns_record.a", plancheck.ResourceActionCreate),
					},
				},
				Check: checkStored(r, "A", "nas.home.internal", func(fakerouter.Record) error { return nil }),
			},
		},
	})
}

func TestRecordValidation(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	cases := map[string]struct{ body, want string }{
		"ttl on MX":        {`type = "MX"` + "\n" + `name = "home.internal"` + "\n" + `value = "mail.home.internal"` + "\n" + `ttl = 60`, `ttl cannot be set for MX records \(only A, AAAA, CNAME records\)`},
		"port on A":        {`type = "A"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "192.168.1.5"` + "\n" + `port = 80`, `port cannot be set for A records`},
		"bad IPv4":         {`type = "A"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "999.1.1.1"`, `must be an IPv4 address`},
		"IPv4 in AAAA":     {`type = "AAAA"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "192.168.1.5"`, `must be an IPv6 address`},
		"NS hostname":      {`type = "NS"` + "\n" + `name = "lab.home.internal"` + "\n" + `value = "ns1.home.internal"`, `conditional forwarders`},
		"CNAME to itself":  {`type = "CNAME"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "x.home.internal"`, `cannot point to itself`},
		"TXT inner quote":  {`type = "TXT"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "say \"hi\" now"`, `double quotes are only allowed around the whole value`},
		"TXT too long":     {`type = "TXT"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "` + regexp.QuoteMeta(fmt.Sprintf("%0256d", 0)) + `"`, `at most 255 characters`},
		"SRV name":         {`type = "SRV"` + "\n" + `name = "sip.home.internal"` + "\n" + `value = "pbx.home.internal"`, `_service._protocol.domain`},
		"comma in name":    {`type = "A"` + "\n" + `name = "a,b.home.internal"` + "\n" + `value = "192.168.1.5"`, `must not be empty or contain whitespace or commas`},
		"unknown type":     {`type = "PTR"` + "\n" + `name = "x.home.internal"` + "\n" + `value = "y"`, `value must be one of`},
		"priority too big": {`type = "MX"` + "\n" + `name = "home.internal"` + "\n" + `value = "mail.home.internal"` + "\n" + `priority = 70000`, `between 0 and 65535`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories,
				Steps: []resource.TestStep{{
					Config:      fakeProviderConfig(r) + "resource \"dreamrouter_dns_record\" \"x\" {\n" + tc.body + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
	if n := len(r.Records()); n != 0 {
		t.Fatalf("validation failures reached the router: %d records stored", n)
	}
}

func TestImportErrorsAndDuplicates(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.Put(fakerouter.Record{RecordType: "A", Key: "rr.home.internal", Value: "192.168.1.10", Enabled: true})
	r.Put(fakerouter.Record{RecordType: "A", Key: "rr.home.internal", Value: "192.168.1.11", Enabled: true})
	cfg := fakeProviderConfig(r) + `
resource "dreamrouter_dns_record" "rr" {
  type  = "A"
  name  = "rr.home.internal"
  value = "192.168.1.11"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{
				// Creating a record that already exists explains how to import it.
				Config:      cfg,
				ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import <address> A/rr.home.internal/192.168.1.11`),
			},
			{
				Config:        cfg,
				ResourceName:  "dreamrouter_dns_record.rr",
				ImportState:   true,
				ImportStateId: "A/rr.home.internal",
				ExpectError:   regexp.MustCompile(`(?s)matches 2 records.*A/rr.home.internal/192.168.1.10`),
			},
			{
				Config:        cfg,
				ResourceName:  "dreamrouter_dns_record.rr",
				ImportState:   true,
				ImportStateId: "A/missing.home.internal",
				ExpectError:   regexp.MustCompile(`No record matches`),
			},
			{
				Config:             cfg,
				ResourceName:       "dreamrouter_dns_record.rr",
				ImportState:        true,
				ImportStateId:      "A/rr.home.internal/192.168.1.11",
				ImportStatePersist: true,
			},
			{
				// After import, the configuration matches: nothing to do.
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("dreamrouter_dns_record.rr", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
}

func TestRecordsDataSource(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	r.Put(fakerouter.Record{RecordType: "A", Key: "b.home.internal", Value: "192.168.1.2", Enabled: true})
	r.Put(fakerouter.Record{RecordType: "A", Key: "a.home.internal", Value: "192.168.1.1", Enabled: true})
	r.Put(fakerouter.Record{RecordType: "MX", Key: "home.internal", Value: "mail.home.internal", Priority: 10, Enabled: true})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: fakeProviderConfig(r) + `
data "dreamrouter_dns_records" "all" {}
data "dreamrouter_dns_records" "a" { type = "A" }
data "dreamrouter_dns_records" "one" { name = "home.internal" }
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.dreamrouter_dns_records.all", "records.#", "3"),
				resource.TestCheckResourceAttr("data.dreamrouter_dns_records.a", "records.#", "2"),
				resource.TestCheckResourceAttr("data.dreamrouter_dns_records.a", "records.0.name", "a.home.internal"),
				resource.TestCheckResourceAttr("data.dreamrouter_dns_records.one", "records.#", "1"),
				resource.TestCheckResourceAttr("data.dreamrouter_dns_records.one", "records.0.priority", "10"),
			),
		}},
	})
}

func TestMissingPassword(t *testing.T) {
	t.Setenv("DREAMROUTER_PASSWORD", "")
	t.Setenv("UNIFI_PASS", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config:      `data "dreamrouter_dns_records" "all" {}`,
			ExpectError: regexp.MustCompile(`Missing router password`),
		}},
	})
}
