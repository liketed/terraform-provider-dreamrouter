package provider

// Acceptance tests run against a real router and only when TF_ACC=1 is set:
//
//	DREAMROUTER_PASSWORD=... TF_ACC=1 go test ./internal/provider -run TestAcc -v
//
// They create temporary records under acc.tftest.internal and destroy them at
// the end. DREAMROUTER_HOST and DREAMROUTER_USERNAME default to 192.168.1.1
// and admin, as in the provider.

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/liketed/terraform-provider-dreamrouter/internal/client"
)

const accDomain = "acc.tftest.internal"

func accHost() string {
	return firstNonEmpty(os.Getenv("DREAMROUTER_HOST"), defaultHost)
}

func accPreCheck(t *testing.T) {
	if firstNonEmpty(os.Getenv("DREAMROUTER_PASSWORD"), os.Getenv("UNIFI_PASS")) == "" {
		t.Fatal("DREAMROUTER_PASSWORD (or UNIFI_PASS) must be set for acceptance tests")
	}
}

func accClient(t *testing.T) *client.Client {
	c, err := client.New(client.Config{
		Host:               accHost(),
		Username:           firstNonEmpty(os.Getenv("DREAMROUTER_USERNAME"), os.Getenv("UNIFI_USER"), defaultUsername),
		Password:           firstNonEmpty(os.Getenv("DREAMROUTER_PASSWORD"), os.Getenv("UNIFI_PASS")),
		InsecureSkipVerify: true,
		CacheTTL:           time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// accCheckNoTestRecords fails if any acc.tftest.internal record is left on the router.
func accCheckNoTestRecords(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		records, err := accClient(t).List(context.Background())
		if err != nil {
			return err
		}
		for _, r := range records {
			if strings.HasSuffix(strings.ToLower(r.Key), accDomain) {
				return fmt.Errorf("record left on router: %s %s %s", r.RecordType, r.Key, r.Value)
			}
		}
		return nil
	}
}

// accCheckResolves polls the router's DNS server until qtype/qname returns want.
func accCheckResolves(qtype, qname, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, net.JoinHostPort(accHost(), "53"))
		}}
		deadline := time.Now().Add(2 * time.Minute)
		var got []string
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			got = got[:0]
			switch qtype {
			case "A", "AAAA":
				addrs, _ := res.LookupIPAddr(ctx, qname)
				for _, a := range addrs {
					got = append(got, a.IP.String())
				}
			case "CNAME":
				if c, err := res.LookupCNAME(ctx, qname); err == nil {
					got = append(got, strings.TrimSuffix(c, "."))
				}
			case "MX":
				mxs, _ := res.LookupMX(ctx, qname)
				for _, m := range mxs {
					got = append(got, fmt.Sprintf("%d %s", m.Pref, strings.TrimSuffix(m.Host, ".")))
				}
			case "TXT":
				got, _ = res.LookupTXT(ctx, qname)
			case "SRV":
				_, srvs, _ := res.LookupSRV(ctx, "", "", qname)
				for _, s := range srvs {
					got = append(got, fmt.Sprintf("%d %d %d %s", s.Priority, s.Weight, s.Port, strings.TrimSuffix(s.Target, ".")))
				}
			}
			cancel()
			for _, g := range got {
				if g == want {
					return nil
				}
			}
			time.Sleep(2 * time.Second)
		}
		return fmt.Errorf("%s %s: router answered %v, want %q", qtype, qname, got, want)
	}
}

func accConfig(aValue string, srvPort int) string {
	return fmt.Sprintf(`
provider "dreamrouter" {}

resource "dreamrouter_dns_record" "a" {
  type  = "A"
  name  = "host.%[1]s"
  value = %[2]q
}
resource "dreamrouter_dns_record" "aaaa" {
  type  = "AAAA"
  name  = "host.%[1]s"
  value = "fd00::230"
  ttl   = 300
}
resource "dreamrouter_dns_record" "cname" {
  type  = "CNAME"
  name  = "alias.%[1]s"
  value = dreamrouter_dns_record.a.name
}
resource "dreamrouter_dns_record" "mx" {
  type     = "MX"
  name     = "%[1]s"
  value    = "mail.%[1]s"
  priority = 10
}
resource "dreamrouter_dns_record" "ns" {
  type  = "NS"
  name  = "lab.%[1]s"
  value = "192.168.1.2"
}
resource "dreamrouter_dns_record" "srv" {
  type     = "SRV"
  name     = "_sip._tcp.%[1]s"
  value    = "pbx.%[1]s"
  priority = 10
  weight   = 5
  port     = %[3]d
}
resource "dreamrouter_dns_record" "txt" {
  type  = "TXT"
  name  = "txt.%[1]s"
  value = "v=spf1 -all"
}

data "dreamrouter_dns_records" "acc" {
  type       = "A"
  name       = "host.%[1]s"
  depends_on = [dreamrouter_dns_record.a]
}
`, accDomain, aValue, srvPort)
}

func TestAccAllRecordTypes(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             accCheckNoTestRecords(t),
		Steps: []resource.TestStep{
			{
				Config: accConfig("192.168.1.230", 5060),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("dreamrouter_dns_record.a", "id"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.acc", "records.#", "1"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.acc", "records.0.value", "192.168.1.230"),
					accCheckResolves("A", "host."+accDomain, "192.168.1.230"),
					accCheckResolves("AAAA", "host."+accDomain, "fd00::230"),
					accCheckResolves("CNAME", "alias."+accDomain, "host."+accDomain),
					accCheckResolves("MX", accDomain, "10 mail."+accDomain),
					accCheckResolves("SRV", "_sip._tcp."+accDomain, "10 5 5060 pbx."+accDomain),
					accCheckResolves("TXT", "txt."+accDomain, "v=spf1 -all"),
				),
			},
			{
				Config: accConfig("192.168.1.231", 5061),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("dreamrouter_dns_record.a", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("dreamrouter_dns_record.srv", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("dreamrouter_dns_record.txt", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					accCheckResolves("A", "host."+accDomain, "192.168.1.231"),
					accCheckResolves("SRV", "_sip._tcp."+accDomain, "10 5 5061 pbx."+accDomain),
				),
			},
			{
				ResourceName:      "dreamrouter_dns_record.srv",
				ImportState:       true,
				ImportStateId:     "SRV/_sip._tcp." + accDomain,
				ImportStateVerify: true,
			},
			{
				ResourceName:      "dreamrouter_dns_record.ns",
				ImportState:       true,
				ImportStateId:     "NS/lab." + accDomain,
				ImportStateVerify: true,
			},
		},
	})
}
