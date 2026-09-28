package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/liketed/dreamrouter-go/fakerouter"
	"github.com/liketed/dreamrouter-go/unifi"
)

// useUpLogins makes the fake router refuse further logins, as if the
// per-minute limit had been reached.
func useUpLogins(t *testing.T, r *fakerouter.Router) {
	t.Helper()
	r.SetLoginLimit(r.LoginCount() + 1)
	c, err := unifi.New(unifi.Config{Host: r.Host(), Username: fakerouter.Username,
		Password: fakerouter.Password, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDNS(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRetryTimeoutSetting(t *testing.T) {
	r := fakerouter.New()
	defer r.Close()
	withTimeout := func(v string) string {
		return strings.Replace(fakeProviderConfig(r), "insecure = true",
			fmt.Sprintf("insecure = true\n  login_retry_timeout = %q", v), 1) +
			`data "dreamrouter_dns_records" "all" {}`
	}
	useUpLogins(t, r)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: providerFactories,
		Steps: []resource.TestStep{
			{Config: withTimeout("soon"), ExpectError: regexp.MustCompile(`Invalid login_retry_timeout`)},
			{Config: withTimeout("-5s"), ExpectError: regexp.MustCompile(`Invalid login_retry_timeout`)},
			// "0" disables retrying: the 429 is reported straight away.
			{Config: withTimeout("0"), ExpectError: regexp.MustCompile(`(?s)HTTP\s+429.*success\.login\.limit\.count`)},
		},
	})
}

// TestLoginLimitWithTerraformCLI runs the real terraform binary against a
// built provider while the fake router refuses logins, and checks that the
// apply waits and succeeds, the warning appears in normal output, and the
// "router login limit reached" message appears in TF_LOG output.
func TestLoginLimitWithTerraformCLI(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "terraform-provider-dreamrouter"), "../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building provider: %v\n%s", err, out)
	}
	r := fakerouter.New()
	defer r.Close()
	writeFile(t, filepath.Join(dir, "dev.tfrc"), fmt.Sprintf(`provider_installation {
  dev_overrides { "liketed/dreamrouter" = %q }
  direct {}
}`, dir))
	writeFile(t, filepath.Join(dir, "main.tf"), `terraform {
  required_providers { dreamrouter = { source = "liketed/dreamrouter" } }
}
`+fakeProviderConfig(r)+`
resource "dreamrouter_dns_record" "nas" {
  type  = "A"
  name  = "nas.home.internal"
  value = "192.168.1.50"
}
`)

	useUpLogins(t, r)
	go func() {
		time.Sleep(3 * time.Second)
		r.SetLoginLimit(0) // the router's per-minute window resets
	}()
	cmd := exec.Command(tf, "apply", "-auto-approve", "-input=false", "-no-color")
	cmd.Dir = dir
	logFile := filepath.Join(dir, "tf.log")
	cmd.Env = append(os.Environ(), "TF_CLI_CONFIG_FILE="+filepath.Join(dir, "dev.tfrc"),
		"TF_LOG_PROVIDER=WARN", "TF_LOG_PATH="+logFile)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("terraform apply failed: %v\n%s", err, out)
	}
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Fatalf("apply finished in %s, before the login limit reset", elapsed)
	}
	if !strings.Contains(string(out), "Warning: Router login limit reached") {
		t.Errorf("warning missing from terraform output:\n%s", out)
	}
	if strings.Count(string(out), "Warning: Router login limit reached") > 1 {
		t.Errorf("warning shown more than once:\n%s", out)
	}
	logs, _ := os.ReadFile(logFile)
	if !strings.Contains(string(logs), "router login limit reached, retrying") {
		t.Errorf("log message missing from TF_LOG output")
	}
	if _, ok := findRecord(r, "A", "nas.home.internal"); !ok {
		t.Errorf("record was not created after the retry")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
