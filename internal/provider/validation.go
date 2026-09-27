package provider

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Record types supported by the UniFi Network application's static DNS.
var recordTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "SRV", "TXT"}

// Which optional numeric fields each type may set to a non-zero value. The
// router rejects others, e.g. "MX record may not have a value set on the
// following parameters: port, ttl, weight".
var allowedFields = map[string]map[string]bool{
	"A":     {"ttl": true},
	"AAAA":  {"ttl": true},
	"CNAME": {"ttl": true},
	"MX":    {"priority": true},
	"NS":    {},
	"SRV":   {"priority": true, "weight": true, "port": true},
	"TXT":   {},
}

var (
	// Names end up in dnsmasq config lines such as host-record=NAME,IP, so
	// whitespace and commas would corrupt them.
	namePattern = regexp.MustCompile(`^[^\s,]+$`)
	srvPattern  = regexp.MustCompile(`^_[^.\s,]+\._[^.\s,]+\.[^\s,]+$`)
)

// recordInput holds the known values of a record's configuration. Unknown
// values (e.g. computed from other resources) are nil and skipped.
type recordInput struct {
	Type, Name, Value *string
	Numbers           map[string]*int64 // ttl, priority, weight, port
}

// validateRecord returns attribute name -> problem for every rule violated.
func validateRecord(in recordInput) map[string]string {
	problems := map[string]string{}
	if in.Name != nil && !namePattern.MatchString(*in.Name) {
		problems["name"] = "must not be empty or contain whitespace or commas"
	}
	if in.Type == nil {
		return problems
	}
	t := *in.Type
	allowed, ok := allowedFields[t]
	if !ok {
		return problems // the schema's OneOf validator reports unknown types
	}
	for field, v := range in.Numbers {
		if v != nil && *v != 0 && !allowed[field] {
			problems[field] = fmt.Sprintf("cannot be set for %s records (only %s)", t, describeAllowed(field))
		}
	}
	if in.Name != nil && t == "SRV" && !srvPattern.MatchString(*in.Name) {
		problems["name"] = `SRV records need a name of the form "_service._protocol.domain", e.g. "_sip._tcp.home.internal"`
	}
	if in.Value == nil {
		return problems
	}
	v := *in.Value
	switch t {
	case "A":
		if a, err := netip.ParseAddr(v); err != nil || !a.Is4() {
			problems["value"] = "must be an IPv4 address for A records"
		}
	case "AAAA":
		if a, err := netip.ParseAddr(v); err != nil || !a.Is6() || a.Is4In6() {
			problems["value"] = "must be an IPv6 address for AAAA records"
		}
	case "NS":
		if _, err := netip.ParseAddr(v); err != nil {
			problems["value"] = "must be the IPv4 or IPv6 address of the DNS server to forward the domain to " +
				"(the router implements NS records as conditional forwarders)"
		}
	case "CNAME", "MX", "SRV":
		if !namePattern.MatchString(v) {
			problems["value"] = fmt.Sprintf("must be a hostname for %s records (no whitespace or commas)", t)
		} else if t == "CNAME" && in.Name != nil && strings.EqualFold(v, *in.Name) {
			problems["value"] = "a CNAME cannot point to itself"
		}
	case "TXT":
		if msg := validateTXT(v); msg != "" {
			problems["value"] = msg
		}
	}
	return problems
}

// validateTXT mirrors the router's rules: double quotes are only allowed
// around the whole value, and each line is at most 255 characters.
func validateTXT(v string) string {
	if v == "" {
		return "must not be empty"
	}
	inner := v
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		inner = v[1 : len(v)-1]
	}
	if strings.Contains(inner, `"`) {
		return `double quotes are only allowed around the whole value, e.g. "\"hello world\""`
	}
	for _, line := range strings.Split(v, "\n") {
		if len(line) > 255 {
			return "each line of a TXT record must be at most 255 characters"
		}
	}
	return ""
}

func describeAllowed(field string) string {
	var types []string
	for _, t := range recordTypes {
		if allowedFields[t][field] {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return "no record types"
	}
	return strings.Join(types, ", ") + " records"
}
