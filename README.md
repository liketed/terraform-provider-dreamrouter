# Terraform provider for Dream Router 7 DNS and DHCP

Manage a UniFi Dream Router 7's (or other UniFi OS gateway's) local network settings with
Terraform or OpenTofu:

- **Static DNS records** (`dreamrouter_dns_record`) of every type the router supports:
  **A, AAAA, CNAME, MX, NS, SRV and TXT**.
- **DHCP reservations** (`dreamrouter_dhcp_reservation`): fixed IP addresses for devices.
- **Hosts** (`dreamrouter_host`): a device's fixed IP and its DNS name, managed together.
- Data sources for the router's DNS names (`dreamrouter_dns_records`) and networks
  (`dreamrouter_networks`).

Everything is managed through the UniFi Network application's own API, so it is the same
as settings made in the web UI (**Settings → Routing → DNS**, and each client's fixed IP
and local DNS record): it appears there, survives reboots and firmware updates, and
reaches the router's DNS and DHCP server (dnsmasq) within about 10–20 seconds of an apply.

The API client and validation are shared with the [drctl](https://github.com/liketed/drctl)
command-line tool, in [dreamrouter-go](https://github.com/liketed/dreamrouter-go).

## Requirements

- Terraform 1.5+ (tested with Terraform 1.15 and 1.16). OpenTofu works too, but the
  provider isn't on the OpenTofu Registry yet; see [OpenTofu](#opentofu).
- A **local** UniFi OS admin account on the router, with permission to change network
  settings. A UI.com cloud account with SSO/2FA will not work.

## Installing

The provider is published on the Terraform Registry as
[`liketed/dreamrouter`](https://registry.terraform.io/providers/liketed/dreamrouter/latest),
with documentation for every resource and data source
([docs](https://registry.terraform.io/providers/liketed/dreamrouter/latest/docs)).
Add it to your configuration and run `terraform init`:

```hcl
terraform {
  required_providers {
    dreamrouter = {
      source  = "liketed/dreamrouter"
      version = "~> 0.1"
    }
  }
}

provider "dreamrouter" {}
```

```bash
export DREAMROUTER_PASSWORD='...'   # see Provider configuration below
terraform init
terraform plan
```

`terraform init` downloads the build for your platform (macOS, Linux, Windows or
FreeBSD, on amd64 or arm64 and more) and checks its signature. Releases are listed on
the [GitHub releases page](https://github.com/liketed/terraform-provider-dreamrouter/releases).

The provider is at version 0.x: its interface may still change between minor versions,
so pin it with `~> 0.1` (any 0.1.x) and read the release notes before moving to 0.2.

### OpenTofu

The provider isn't on the OpenTofu Registry yet. Until it is, OpenTofu users can build it
from source and use a `dev_overrides` block in `~/.tofurc`, as described below.

### Building from source

For local development, or to try unreleased changes, build the provider and tell
Terraform (or OpenTofu) to use your build instead of the registry's:

```bash
# Install the latest commit from GitHub into $(go env GOPATH)/bin (usually ~/go/bin):
go install github.com/liketed/terraform-provider-dreamrouter@latest

# Or build from a clone of this repository:
git clone https://github.com/liketed/terraform-provider-dreamrouter.git
cd terraform-provider-dreamrouter
go build -o terraform-provider-dreamrouter .
```

This needs Go 1.26 or later. Then add a `dev_overrides` block to `~/.terraformrc`
(Terraform) or `~/.tofurc` (OpenTofu), pointing at the **directory** that contains the
binary: `~/go/bin` after `go install`, or the clone's directory after `go build`. Use an
absolute path:

```hcl
provider_installation {
  dev_overrides {
    "liketed/dreamrouter" = "/Users/you/go/bin"
  }
  direct {}
}
```

With `dev_overrides` there is no need to run `terraform init` for this provider, and
Terraform prints a warning that development overrides are in effect, which is expected.
After changing the code, rebuild (or re-run `go install`) and the next `plan` uses the new
binary. Remove the `dev_overrides` entry to go back to the published version.

## Provider configuration

```hcl
provider "dreamrouter" {
  # All optional; shown with their defaults.
  host     = "192.168.1.1"   # or DREAMROUTER_HOST
  username = "admin"         # or DREAMROUTER_USERNAME, then UNIFI_USER
  site     = "default"
  insecure = true            # skip TLS verification (the router's certificate is self-signed)

  login_retry_timeout = "2m" # or DREAMROUTER_LOGIN_RETRY_TIMEOUT; "0" disables retrying
}
```

| Argument | Default | Environment variable | Meaning |
|---|---|---|---|
| `host` | `192.168.1.1` | `DREAMROUTER_HOST` | Router address, optionally with a port. |
| `username` | `admin` | `DREAMROUTER_USERNAME`, `UNIFI_USER` | Local UniFi OS account. |
| `password` | – (required) | `DREAMROUTER_PASSWORD`, `UNIFI_PASS` | Password for that account. |
| `site` | `default` | – | UniFi Network site name. |
| `insecure` | `true` | `DREAMROUTER_INSECURE` | Skip TLS certificate verification. |
| `login_retry_timeout` | `2m` | `DREAMROUTER_LOGIN_RETRY_TIMEOUT` | How long to keep retrying when the router's login limit is reached. See [Login limit](#login-limit). |

Set the password in the environment rather than in configuration:

```bash
read -s DREAMROUTER_PASSWORD; export DREAMROUTER_PASSWORD
```

`UNIFI_PASS` also works, so the same variable can serve the command-line scripts.

## Resource: `dreamrouter_dns_record`

```hcl
resource "dreamrouter_dns_record" "nas" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.50"
}
```

| Argument | Required | Notes |
|---|---|---|
| `type` | yes | `A`, `AAAA`, `CNAME`, `MX`, `NS`, `SRV` or `TXT`. Changing it replaces the record. |
| `name` | yes | e.g. `nas.home.internal`. For SRV: `_service._protocol.domain`, e.g. `_sip._tcp.home.internal`. Changing it replaces the record. |
| `value` | yes | See the table below. |
| `ttl` | no | Seconds, for A, AAAA and CNAME only. `0` (default) means automatic. |
| `priority` | no | MX and SRV only. Default `0`. |
| `weight` | no | SRV only. Default `0`. |
| `port` | no | SRV only. Default `0`. |
| `enabled` | no | Default `true`. A disabled record is kept on the router but not served. |

`id` is exported: the record's ID on the router. Every other change is made in place.

What `value` means for each type, and the rules the router enforces (the provider checks
them during `plan`, before anything is sent):

| Type | `value` | Extra fields | Router behaviour |
|---|---|---|---|
| A | IPv4 address | `ttl` | Several A records may share a name (round robin). |
| AAAA | IPv6 address | `ttl` | |
| CNAME | target hostname | `ttl` | A CNAME can't share its name with any other record, can't point to itself, and chains can't loop. |
| MX | mail server hostname | `priority` | |
| NS | **IP address** of a DNS server | – | The router implements NS records as conditional forwarders (dnsmasq `server=/name/ip`): queries for the name and its subdomains are forwarded to that server. |
| SRV | target hostname | `priority`, `weight`, `port` | |
| TXT | text | – | Double quotes are only allowed around the whole value (`"\"hello world\""`), and each line is at most 255 characters. |

### Examples

```hcl
locals {
  hosts = {
    "nas.home.internal"     = "192.168.1.50"
    "printer.home.internal" = "192.168.1.60"
  }
}

resource "dreamrouter_dns_record" "host" {
  for_each = local.hosts
  type     = "A"
  name     = each.key
  value    = each.value
}

resource "dreamrouter_dns_record" "files" {
  type  = "CNAME"
  name  = "files.home.internal"
  value = dreamrouter_dns_record.host["nas.home.internal"].name
  ttl   = 300
}

resource "dreamrouter_dns_record" "sip" {
  type     = "SRV"
  name     = "_sip._tcp.home.internal"
  value    = "pbx.home.internal"
  priority = 10
  weight   = 5
  port     = 5060
}
```

A fuller example with every type is in [`examples/basic/main.tf`](examples/basic/main.tf).

### Import

Existing records (for example ones created in the web UI or with the command-line
scripts) can be brought under Terraform without recreating them. The import ID is
`TYPE/name`, or `TYPE/name/value` when several records share a type and name:

```bash
terraform import 'dreamrouter_dns_record.host["nas.home.internal"]' A/nas.home.internal
terraform import dreamrouter_dns_record.rr A/rr.home.internal/192.168.1.11
```

Or with `import` blocks (Terraform 1.5+), which also work with `for_each` (1.7+):

```hcl
import {
  for_each = local.hosts
  to       = dreamrouter_dns_record.host[each.key]
  id       = "A/${each.key}"
}
```

The router's record ID also works as an import ID. If you try to create a record that
already exists, the error message shows the import command to use instead.

### Drift

`terraform plan` compares every managed record with the router. A record edited in the
web UI is changed back in place; a record deleted in the web UI is recreated.

### Several Terraform projects, and records created elsewhere

Each project only manages the records in its own state. Records created by other
Terraform projects, in the web UI, or with drctl are left alone:
they don't appear in `plan`, aren't warned about, and are never changed or deleted,
including by `terraform destroy`. Several projects can therefore share one router, and
can even use the same name with different record types or values (e.g. an A record in
one project and an AAAA record for the same name in another).

A given record (same type, name and value) should belong to **one** project:

- If a second project tries to create a record another project already created, the
  router rejects it as a duplicate and the apply fails with an "already exists" error;
  nothing is changed or deleted. Remove the record from one of the configurations.
- Don't `terraform import` a record that another project manages. Both projects would
  then own it, and destroying either one would delete it for both.

A static record can't use a name that is already a device's DNS name (see
[`dreamrouter_host`](#resource-dreamrouter_host)); the apply fails with an error saying so.

## Resource: `dreamrouter_dhcp_reservation`

A fixed IP address for a device, identified by its MAC address.

```hcl
resource "dreamrouter_dhcp_reservation" "printer" {
  mac  = "aa:bb:cc:dd:ee:02"
  ip   = "192.168.1.60"
  name = "printer"   # optional: the name shown in the web UI
}
```

| Argument | Required | Notes |
|---|---|---|
| `mac` | yes | Lower-case colon form, `aa:bb:cc:dd:ee:ff` (the form the router uses; other forms are rejected with the correct spelling). Changing it replaces the reservation. |
| `ip` | yes | Must be in one of the router's networks, not the router's own, network or broadcast address, and not reserved for another device. Changed in place. |
| `name` | no | Device name shown in the web UI. If not set, a device the router already knows keeps its name. |
| `network_id` | no | Network the IP belongs to (see [`dreamrouter_networks`](#data-source-dreamrouter_networks)). Defaults to the network whose subnet contains `ip`. |
| `forget_on_destroy` | no | Default `false`: destroy removes only the reservation and keeps the device (its name and history). `true` removes the device from the router entirely. |

- A device the router doesn't know yet (never connected) is created with the reservation.
- The provider **won't take over a device that already has a reservation** (made in the
  web UI, with drctl, or by another project). The apply fails and shows the
  `terraform import` command to use if nothing else manages it.
- If another device currently uses the IP, the apply warns: that device gets another
  address when its lease renews.
- Drift: a changed IP is set back; a reservation removed in the web UI is recreated.
- Import by MAC address: `terraform import dreamrouter_dhcp_reservation.printer aa:bb:cc:dd:ee:02`.

## Resource: `dreamrouter_host`

A device with a fixed IP address **and** a DNS name. The router serves the name like an A
record, but stores it on the device and only serves it while the device has a fixed IP,
so the two are managed together.

```hcl
resource "dreamrouter_host" "nas" {
  name = "nas.home.internal"
  ip   = "192.168.1.50"
  mac  = "aa:bb:cc:dd:ee:01"
}

resource "dreamrouter_dns_record" "files" {
  type  = "CNAME"
  name  = "files.home.internal"
  value = dreamrouter_host.nas.name
}
```

`ip`, `mac`, `network_id` and `forget_on_destroy` work as for
`dreamrouter_dhcp_reservation`. `name` is the DNS name; changing it renames the host in
place. It can't be a static DNS record's name or another device's DNS name: the apply
fails and says which record is in the way. `device_name` sets the name shown in the web
UI; a new device is labelled with the host name.

Use either `dreamrouter_host` or `dreamrouter_dhcp_reservation` for a device, not both.
Drift: a changed IP or name is set back, a DNS name turned off in the web UI is turned
back on, and a removed reservation is recreated. Import by host name (or MAC address):
`terraform import dreamrouter_host.nas nas.home.internal`.

## Data source: `dreamrouter_dns_records`

Lists the names the router answers for: static DNS records and devices' DNS names,
including ones Terraform doesn't manage. It is read-only: listing a record doesn't make
the project manage it.

```hcl
data "dreamrouter_dns_records" "all" {}

data "dreamrouter_dns_records" "mail" {
  type        = "MX"             # optional filter
  name        = "home.internal"  # optional filter, exact match
  static_only = true             # optional: leave out devices' DNS names
}

output "all_records" {
  value = [for r in data.dreamrouter_dns_records.all.records : "${r.type} ${r.name} -> ${r.value}"]
}
```

Each entry in `records` has `id`, `type`, `name`, `value`, `ttl`, `priority`, `weight`,
`port`, `enabled`, `source` and `mac`, sorted by name, type and value. `source` is
`"static"` for a static record, or `"host"` for a device's DNS name (type `A`, with the
device's `mac`).

## Data source: `dreamrouter_networks`

Lists the router's networks, e.g. to find the `network_id` for a reservation on a
network other than the default.

```hcl
data "dreamrouter_networks" "iot" {
  name = "IoT"   # optional; fails if there is no such network
}

resource "dreamrouter_dhcp_reservation" "sensor" {
  mac        = "aa:bb:cc:dd:ee:20"
  ip         = "10.0.20.5"
  network_id = data.dreamrouter_networks.iot.networks[0].id
}
```

Each entry in `networks` has `id`, `name`, `purpose`, `subnet` (e.g. `192.168.1.1/24`),
`vlan`, `dhcp_enabled`, `dhcp_start`, `dhcp_stop` and `domain_name`.

## Login limit

The router allows **5 successful logins per minute** by default. The limit is enforced by
UniFi OS's identity service, ulp-go, and set by `success.login.limit.count` in
`/usr/lib/ulp-go/config.props` on the router (a firmware update resets it to 5).

The provider logs in only when it first needs to, then reuses the session for every
resource. Measured with 50 records:

| Command | Logins |
|---|---|
| `terraform plan` with only new records | 0 |
| `terraform plan` or `apply` with no changes | 1 |
| `terraform apply` creating 50 records | 1 |
| `terraform destroy` of 50 records | 2 |

So normal use costs 1–2 logins per command regardless of the number of records, and a
plan followed by an apply fits comfortably within the default limit.

**When the limit is reached**, the router answers the login with HTTP 429. The provider
then waits and retries (after 5 s, 10 s, then every 20 s) for up to `login_retry_timeout`
(default `2m`). The router's window is one minute, so a burst of commands slows down
instead of failing. Each wait is logged as a warning (Terraform adds a timestamp and some
internal fields):

```
2026-09-27T20:23:57.796+0100 [WARN]  provider.terraform-provider-dreamrouter: router login limit reached, retrying in 5s: ...
```

Terraform only shows provider logs when asked, e.g. `TF_LOG_PROVIDER=WARN terraform apply`.
So that a wait doesn't go unnoticed, the provider also adds one warning to the normal
`plan`/`apply` output when it had to wait:

```
Warning: Router login limit reached

The router refused to log in because its login limit was reached, so the provider
waited 15s before retrying. The limit is set by success.login.limit.count in
/usr/lib/ulp-go/config.props on the router (default 5 logins per minute).
```

If the limit is still reached when `login_retry_timeout` runs out, or immediately with
`login_retry_timeout = "0"`, the command fails with the router's HTTP 429 error and a
pointer to the setting above. A wrong password is never retried.

If you routinely run many commands in quick succession (scripts, CI, the acceptance
tests), consider raising the limit on the router. The
[drctl README](https://github.com/liketed/drctl#login-rate-limiting) describes how.

## How it talks to the router

- **One login per Terraform command,** retried if the login limit is reached; see
  [Login limit](#login-limit). The provider logs in again automatically if the session
  expires.
- **One list call per command, shared.** The API has no way to fetch a single record or
  device, so reading means listing them all. The provider lists once, shares the result
  between all resources, and refreshes it after each change it makes. Listing 1000
  records takes about 0.2 s.
- **Changes are sent one at a time.** Each change makes the Network application rebuild
  and push the DNS configuration to the router; sending them one after another avoids
  overlapping provisioning runs. Each takes about 60 ms, so 50 records apply in a few
  seconds.
- **Propagation.** An apply finishes when the Network application has stored the change.
  The router's DNS server picks it up about 10–20 seconds later.
- The router uses a self-signed certificate, so TLS verification is off by default
  (`insecure = true`). Only point the provider at a router on a network you trust, or set
  `insecure = false` if you've installed a trusted certificate.
- It uses the Network application's internal (undocumented) API, the same one the web UI
  uses; a future UniFi Network update could change it.

## Development

```bash
go test ./...                     # unit tests, against an in-memory fake router
go vet ./... && gofmt -l .
```

The unit tests need the `terraform` binary on `PATH` (they run real Terraform commands
against the provider) but no network access. They use the in-memory fake router from
[dreamrouter-go](https://github.com/liketed/dreamrouter-go)'s `fakerouter` package, which
mimics the router's API, including its error codes, session handling and login limit.

Acceptance tests run against a real router and only when `TF_ACC=1` is set. They create
temporary records under `acc.tftest.internal` and a reservation and a host for made-up
MAC addresses (`02:00:00:dd:cc:1x`, removed entirely afterwards), check that the router's
DNS server answers for them, and destroy them at the end:

```bash
DREAMROUTER_PASSWORD=... TF_ACC=1 go test ./internal/provider -run TestAcc -v
```

One acceptance run executes many Terraform commands (plan, apply, refresh and import for
each step) in about 30 seconds, so it logs in more than 5 times. On a router with a
raised limit (for example 60) it takes about 30 seconds. With the default limit of 5,
expect the login retries to kick in and the run to take a few minutes longer. (The retry
behaviour itself is covered by tests against the fake router; the acceptance tests have
not yet been run on a real router at the default limit.)

Layout:

| Path | Contents |
|---|---|
| `main.go` | Provider entry point (source address `liketed/dreamrouter`). |
| `internal/provider` | Provider, resources, data sources and tests. The API client, validation and fake router come from [dreamrouter-go](https://github.com/liketed/dreamrouter-go). |
| `examples/basic` | Example configuration using every record type. |
| `examples/provider`, `examples/resources`, `examples/data-sources` | Examples used in the generated docs. |
| `templates/` | Doc templates for the provider and resource pages. |
| `docs/` | Generated registry documentation (don't edit by hand; see below). |
| `terraform-registry-manifest.json` | Declares plugin protocol 6.0 for the registries. |
| `.goreleaser.yml` | Release build: zips per OS/arch, `SHA256SUMS` and its GPG signature. |
| `.github/workflows/` | `test.yml` (build, vet, format, unit tests, docs check) and `release.yml` (publish on `v*` tags). |

### Documentation

`docs/` is generated by [tfplugindocs](https://github.com/hashicorp/terraform-plugin-docs)
(pinned as a Go tool dependency in `go.mod`) from the schema descriptions in the code,
`examples/` and `templates/`. After changing any of those, regenerate and commit the result:

```bash
go generate ./...
go tool tfplugindocs validate --provider-name dreamrouter
```

The Test workflow fails if `docs/` is out of date.

## Releasing

Releases are built and signed by GitHub Actions when a version tag is pushed. The
Terraform Registry picks them up from GitHub automatically (it is set up for this
repository); the OpenTofu Registry will too once the provider is submitted there.

One-time setup (already done for the Terraform Registry; kept here for reference):

1. Create an **RSA** GPG key for signing (the Terraform Registry doesn't accept ECC keys):
   `gpg --full-generate-key`, choosing RSA and RSA, 4096 bits.
2. In the GitHub repository, under Settings → Secrets and variables → Actions, add:
   - `GPG_PRIVATE_KEY`: the output of `gpg --armor --export-secret-keys <key-id>`
   - `PASSPHRASE`: the key's passphrase
3. Terraform Registry: sign in to registry.terraform.io with GitHub, add the public key
   (`gpg --armor --export <key-id>`) under User Settings → Signing Keys, then
   Publish → Provider and choose this repository.
4. OpenTofu Registry: open a "Submit new provider" issue in the
   [opentofu/registry](https://github.com/opentofu/registry) repository, and optionally a
   "Submit new provider signing key" issue with the public key.

To release, tag a semantic version with a `v` prefix and push it:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The Release workflow builds zips for macOS, Linux, Windows and FreeBSD, signs the
`SHA256SUMS` file, and publishes a GitHub release. To try the release build locally
without signing or publishing:

```bash
goreleaser release --snapshot --clean --skip=sign,publish   # output in dist/
```

## License

Licensed under the [Apache License, Version 2.0](LICENSE) (SPDX: `Apache-2.0`).

You may use, modify and distribute this provider, including in commercial settings,
provided you keep the license and any copyright notices, and state significant changes
you make to the files. It is provided "as is", without warranties or conditions of any
kind; see the [LICENSE](LICENSE) file for the full terms.
