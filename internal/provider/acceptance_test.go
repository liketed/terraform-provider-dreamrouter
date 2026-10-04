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

	"github.com/liketed/dreamrouter-go/unifi"
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

func accClient(t *testing.T) *unifi.Client {
	c, err := unifi.New(unifi.Config{
		Host:               accHost(),
		Username:           firstNonEmpty(os.Getenv("DREAMROUTER_USERNAME"), os.Getenv("UNIFI_USER"), defaultUsername),
		Password:           firstNonEmpty(os.Getenv("DREAMROUTER_PASSWORD"), os.Getenv("UNIFI_PASS")),
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// accCheckNoTestRecords fails if any acc.tftest.internal record is left on the router.
func accCheckNoTestRecords(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		records, err := accClient(t).ListDNS(context.Background())
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

// accCheckNoTestDevices fails if a test device is left on the router.
func accCheckNoTestDevices(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		clients, err := accClient(t).ListClients(context.Background())
		if err != nil {
			return err
		}
		for _, d := range clients {
			if strings.HasPrefix(d.MAC, "02:00:00:dd:cc:1") {
				return fmt.Errorf("test device left on router: %s %s", d.MAC, d.DisplayName())
			}
		}
		return nil
	}
}

func accReservationConfig(hostIP string) string {
	return fmt.Sprintf(`
provider "dreamrouter" {}

data "dreamrouter_networks" "lan" {
  name = "Default"
}

resource "dreamrouter_dhcp_reservation" "r" {
  mac               = "02:00:00:dd:cc:11"
  ip                = "192.168.1.248"
  name              = "tftest-reservation"
  network_id        = data.dreamrouter_networks.lan.networks[0].id
  forget_on_destroy = true
}

resource "dreamrouter_host" "h" {
  name              = "host.%[1]s"
  ip                = %[2]q
  mac               = "02:00:00:dd:cc:12"
  forget_on_destroy = true
}

data "dreamrouter_dns_records" "hosts" {
  name       = "host.%[1]s"
  depends_on = [dreamrouter_host.h]
}
`, accDomain, hostIP)
}

func TestAccReservationsAndHosts(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             resource.ComposeAggregateTestCheckFunc(accCheckNoTestDevices(t), accCheckNoTestRecords(t)),
		Steps: []resource.TestStep{
			{
				Config: accReservationConfig("192.168.1.249"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("dreamrouter_dhcp_reservation.r", "network_id", "data.dreamrouter_networks.lan", "networks.0.id"),
					resource.TestCheckResourceAttr("dreamrouter_host.h", "device_name", "host."+accDomain),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.hosts", "records.0.source", "host"),
					resource.TestCheckResourceAttr("data.dreamrouter_dns_records.hosts", "records.0.value", "192.168.1.249"),
					accCheckResolves("A", "host."+accDomain, "192.168.1.249"),
				),
			},
			{
				// Moving the host is an in-place update, and DNS follows.
				Config: accReservationConfig("192.168.1.247"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_host.h", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("dreamrouter_dhcp_reservation.r", plancheck.ResourceActionNoop),
				}},
				Check: accCheckResolves("A", "host."+accDomain, "192.168.1.247"),
			},
			{
				ResourceName:            "dreamrouter_host.h",
				ImportState:             true,
				ImportStateId:           "host." + accDomain,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"forget_on_destroy"},
			},
			{
				ResourceName:            "dreamrouter_dhcp_reservation.r",
				ImportState:             true,
				ImportStateId:           "02:00:00:dd:cc:11",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"forget_on_destroy"},
			},
		},
	})
}

// accCheckNetworkBootOff fails unless the Default network has network boot
// off and no TFTP server, as after destroying dreamrouter_network_dhcp.
func accCheckNetworkBootOff(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		networks, err := accClient(t).ListNetworks(context.Background())
		if err != nil {
			return err
		}
		for _, n := range networks {
			if n.Name == "Default" && (n.BootEnabled || n.TFTPServer != "" || n.BootServer != "") {
				return fmt.Errorf("network Default still has boot=%v server=%q tftp=%q", n.BootEnabled, n.BootServer, n.TFTPServer)
			}
		}
		return nil
	}
}

func accNetworkDHCPConfig(file string) string {
	return fmt.Sprintf(`
provider "dreamrouter" {}

resource "dreamrouter_network_dhcp" "lan" {
  network = "Default"
  boot = {
    server = "192.168.1.249"
    file   = %q
  }
  tftp_server = "tftp.tftest.invalid"
}
`, file)
}

// TestAccNetworkDHCP changes network boot on the router's default network and
// ends with it OFF. Don't run it against a network whose network boot is in
// use: destroying a dreamrouter_network_dhcp turns network boot off.
func TestAccNetworkDHCP(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             accCheckNetworkBootOff(t),
		Steps: []resource.TestStep{
			{
				Config: accNetworkDHCPConfig("tf-acc.efi"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_network_dhcp.lan", "boot.server", "192.168.1.249"),
					resource.TestCheckResourceAttr("dreamrouter_network_dhcp.lan", "tftp_server", "tftp.tftest.invalid"),
				),
			},
			{
				Config: accNetworkDHCPConfig("tf-acc2.efi"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_network_dhcp.lan", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("dreamrouter_network_dhcp.lan", "boot.file", "tf-acc2.efi"),
			},
			{
				ResourceName:            "dreamrouter_network_dhcp.lan",
				ImportState:             true,
				ImportStateId:           "Default",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"network"},
			},
		},
	})
}

// TestAccLeases is read-only: it lists the router's real leases.
func TestAccLeases(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "dreamrouter" {}
data "dreamrouter_leases" "all" {}
data "dreamrouter_leases" "online" { status = "online" }
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.dreamrouter_leases.all", "leases.0.ip"),
				resource.TestCheckResourceAttrSet("data.dreamrouter_leases.all", "leases.0.mac"),
				resource.TestCheckResourceAttrSet("data.dreamrouter_leases.online", "leases.0.ip"),
			),
		}},
	})
}

// accCheckBlockDevice checks whether the made-up device 02:00:00:dd:cc:41
// has a record, and if so whether it is blocked.
func accCheckBlockDevice(t *testing.T, wantRecord, wantBlocked bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		clients, err := accClient(t).ListClients(context.Background())
		if err != nil {
			return err
		}
		for _, d := range clients {
			if d.MAC == "02:00:00:dd:cc:41" {
				switch {
				case !wantRecord:
					return fmt.Errorf("record for 02:00:00:dd:cc:41 left on the router: %+v", d)
				case d.Blocked != wantBlocked:
					return fmt.Errorf("02:00:00:dd:cc:41 blocked = %v, want %v", d.Blocked, wantBlocked)
				}
				return nil
			}
		}
		if wantRecord {
			return fmt.Errorf("no record for 02:00:00:dd:cc:41")
		}
		return nil
	}
}

// TestAccClients reads the device list and blocks a made-up MAC address the
// router doesn't know; destroying unblocks it and removes the record the
// block created.
func TestAccClients(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             accCheckBlockDevice(t, false, false),
		Steps: []resource.TestStep{{
			Config: `
provider "dreamrouter" {}
data "dreamrouter_clients" "online" { status = "online" }
resource "dreamrouter_client_block" "acc" {
  mac = "02:00:00:dd:cc:41"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.dreamrouter_clients.online", "clients.0.mac"),
				resource.TestCheckResourceAttr("data.dreamrouter_clients.online", "clients.0.status", "online"),
				resource.TestCheckResourceAttr("dreamrouter_client_block.acc", "id", "02:00:00:dd:cc:41"),
				accCheckBlockDevice(t, true, true),
			),
		}},
	})
}

func TestAccStatus(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "dreamrouter" {}
data "dreamrouter_status" "router" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet("data.dreamrouter_status.router", "os_version"),
				resource.TestCheckResourceAttrSet("data.dreamrouter_status.router", "wan_ip"),
				resource.TestCheckResourceAttrSet("data.dreamrouter_status.router", "devices.0.version"),
			),
		}},
	})
}

// accCheckNoTestForwards fails if a test port forward (named acc-*) is left.
func accCheckNoTestForwards(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		list, err := accClient(t).ListPortForwards(context.Background())
		if err != nil {
			return err
		}
		for _, pf := range list {
			if strings.HasPrefix(pf.Name, "acc-") {
				return fmt.Errorf("test port forward left on the router: %+v", pf)
			}
		}
		return nil
	}
}

func accPortForwardConfig(port string) string {
	return fmt.Sprintf(`
provider "dreamrouter" {}
resource "dreamrouter_port_forward" "acc" {
  name       = "acc-probe"
  port       = %q
  forward_ip = "192.168.1.250"
  protocol   = "tcp"
  enabled    = false # never opens anything
}
data "dreamrouter_port_forwards" "all" {
  depends_on = [dreamrouter_port_forward.acc]
}
`, port)
}

// TestAccPortForward creates, changes, imports and destroys a disabled
// port forward to an unused address, so nothing is ever opened.
func TestAccPortForward(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy:             accCheckNoTestForwards(t),
		Steps: []resource.TestStep{
			{
				Config: accPortForwardConfig("40001"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_port_forward.acc", "enabled", "false"),
					resource.TestCheckResourceAttrSet("dreamrouter_port_forward.acc", "id"),
					resource.TestCheckTypeSetElemNestedAttrs("data.dreamrouter_port_forwards.all", "port_forwards.*",
						map[string]string{"name": "acc-probe", "port": "40001", "forward_port": "40001", "enabled": "false"}),
				),
			},
			{
				Config: accPortForwardConfig("40010-40020"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_port_forward.acc", plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr("dreamrouter_port_forward.acc", "port", "40010-40020"),
			},
			{
				ResourceName:      "dreamrouter_port_forward.acc",
				ImportState:       true,
				ImportStateId:     "acc-probe",
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccSSH manages both SSH settings with the values they already have
// (both on), so nothing on the router changes; destroying leaves SSH alone.
func TestAccSSH(t *testing.T) {
	cfg := `
provider "dreamrouter" {}
resource "dreamrouter_ssh" "acc" {
  router  = true
  devices = true
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accPreCheck(t) },
		ProtoV6ProviderFactories: providerFactories,
		CheckDestroy: func(*terraform.State) error {
			s, err := accClient(t).GetSSH(context.Background())
			if err != nil {
				return err
			}
			if !s.Router || !s.Devices {
				return fmt.Errorf("SSH settings changed: %+v", s)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dreamrouter_ssh.acc", "router", "true"),
					resource.TestCheckResourceAttr("dreamrouter_ssh.acc", "devices", "true"),
				),
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("dreamrouter_ssh.acc", plancheck.ResourceActionNoop),
				}},
			},
			{ResourceName: "dreamrouter_ssh.acc", ImportState: true, ImportStateId: "ssh", ImportStateVerify: true},
		},
	})
}
