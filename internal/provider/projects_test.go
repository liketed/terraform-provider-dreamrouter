package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/liketed/dreamrouter-go/fakerouter"
)

// TestSeparateProjectsDoNotTouchEachOther runs the real terraform binary for
// two independent projects (separate directories and state) against one
// router that also has records created elsewhere (e.g. in the web UI). Each
// project must only see, report and delete its own records.
func TestSeparateProjectsDoNotTouchEachOther(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "terraform-provider-dreamrouter"), "../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building provider: %v\n%s", err, out)
	}
	r := fakerouter.New()
	defer r.Close()
	r.PutDNS(fakerouter.DNSRecord{RecordType: "A", Key: "ui.home.internal", Value: "192.168.1.5", Enabled: true})
	r.PutDNS(fakerouter.DNSRecord{RecordType: "CNAME", Key: "www.home.internal", Value: "ui.home.internal", Enabled: true})

	tfrc := filepath.Join(binDir, "dev.tfrc")
	writeFile(t, tfrc, fmt.Sprintf(`provider_installation {
  dev_overrides { "liketed/dreamrouter" = %q }
  direct {}
}`, binDir))

	project := func(name, records string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "main.tf"), `terraform {
  required_providers { dreamrouter = { source = "liketed/dreamrouter" } }
}
`+fakeProviderConfig(r)+records)
		return dir
	}
	projectA := project("a", `
resource "dreamrouter_dns_record" "nas" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.50"
}
resource "dreamrouter_dns_record" "mail" {
  type     = "MX"
  name     = "home.internal"
  value    = "mail.home.internal"
  priority = 10
}
`)
	// Project B uses a name project A also uses, with a different type.
	projectB := project("b", `
resource "dreamrouter_dns_record" "printer" {
  type  = "A"
  name  = "printer.home.internal"
  value = "192.168.1.60"
}
resource "dreamrouter_dns_record" "nas_v6" {
  type  = "AAAA"
  name  = "nas.home.internal"
  value = "fd00::50"
}
`)

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(tf, append(args, "-input=false", "-no-color")...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+tfrc)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terraform %s in %s: %v\n%s", args[0], filepath.Base(dir), err, out)
		}
		return string(out)
	}
	stored := func() []string {
		var keys []string
		for _, rec := range r.DNS() {
			keys = append(keys, rec.RecordType+" "+rec.Key)
		}
		sort.Strings(keys)
		return keys
	}
	expectStored := func(step string, want ...string) {
		t.Helper()
		sort.Strings(want)
		if got := stored(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("after %s, router has %q, want %q", step, got, want)
		}
	}
	// Other projects' records must never appear in a plan, and nothing but the
	// dev_overrides notice may be warned about.
	expectQuietPlan := func(step, out string, foreign ...string) {
		t.Helper()
		if !strings.Contains(out, "No changes.") {
			t.Fatalf("%s: expected no changes, got:\n%s", step, out)
		}
		for _, f := range foreign {
			if strings.Contains(out, f) {
				t.Fatalf("%s: plan mentions %q, which this project doesn't manage:\n%s", step, f, out)
			}
		}
		if n := strings.Count(out, "Warning:"); n != 1 || !strings.Contains(out, "Warning: Provider development overrides") {
			t.Fatalf("%s: unexpected warnings:\n%s", step, out)
		}
	}

	ui := []string{"A ui.home.internal", "CNAME www.home.internal"}
	a := []string{"A nas.home.internal", "MX home.internal"}
	b := []string{"A printer.home.internal", "AAAA nas.home.internal"}

	run(projectA, "apply", "-auto-approve")
	run(projectB, "apply", "-auto-approve")
	expectStored("both applies", append(append(append([]string{}, ui...), a...), b...)...)

	expectQuietPlan("plan A", run(projectA, "plan"), "printer", "fd00::50", "ui.home.internal", "www.home.internal")
	expectQuietPlan("plan B", run(projectB, "plan"), "192.168.1.50", "mail.home.internal", "ui.home.internal", "www.home.internal")

	run(projectA, "destroy", "-auto-approve")
	expectStored("destroying A", append(append([]string{}, ui...), b...)...)
	expectQuietPlan("plan B after A destroyed", run(projectB, "plan"))

	run(projectB, "destroy", "-auto-approve")
	expectStored("destroying B", ui...)
}
