package doctor

import (
	"fmt"
	"strings"

	"github.com/cdrrazan/roost/internal/tunnel"
)

// CertLister fetches the SAN hostnames covered by a zone's edge
// certificates (Universal SSL + any ACM packs).
type CertLister func(zoneID string) ([]string, error)

// SubdomainDepth counts how many labels host sits below the zone apex:
// app1.example.com is 1, app1.demo.example.com is 2.
func SubdomainDepth(host, zone string) int {
	if host == zone {
		return 0
	}
	prefix := strings.TrimSuffix(host, "."+zone)
	return strings.Count(prefix, ".") + 1
}

// wildcardCovers reports whether a certificate SAN covers a hostname.
// Wildcards match exactly one label, like in DNS.
func wildcardCovers(san, host string) bool {
	if san == host {
		return true
	}
	if !strings.HasPrefix(san, "*.") {
		return false
	}
	suffix := san[1:] // ".demo.example.com"
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	return label != "" && !strings.Contains(label, ".")
}

// CheckSSLDepth guards against the multi-level subdomain SSL trap:
// free Universal SSL covers the apex and ONE subdomain level only, so
// a hostname two or more levels below the zone apex gets an opaque
// browser TLS error with nothing useful in any log. Depth >= 2 without
// a matching ACM wildcard SAN is a hard finding; when the token can't
// read the zone's certificates the check degrades to a warning rather
// than reporting a false negative.
func CheckSSLDepth(host string, zone tunnel.Zone, listCerts CertLister) Finding {
	const check = "ssl-depth"
	depth := SubdomainDepth(host, zone.Name)
	if depth < 2 {
		return ok(check, fmt.Sprintf("%s is %d level(s) below zone %s; Universal SSL covers it", host, depth, zone.Name))
	}

	sans, err := listCerts(zone.ID)
	if err == nil {
		for _, san := range sans {
			if wildcardCovers(san, host) {
				return ok(check, fmt.Sprintf("%s is %d levels deep but an edge certificate SAN (%s) covers it", host, depth, san))
			}
		}
	}

	// Flattened suggestion: first label directly under the apex.
	first, _, _ := strings.Cut(host, ".")
	flattened := first + "." + zone.Name

	remedy := fmt.Sprintf(
		"pick one: (1) flatten the hostname to %s (free, recommended), (2) serve it from a dedicated one-level domain, or (3) enable Advanced Certificate Manager on %s ($10/month) with the %s level added to the certificate",
		flattened, zone.Name, "*."+strings.SplitN(host, ".", 2)[1])

	if err != nil {
		return warn(check,
			fmt.Sprintf("%s is %d levels below zone %s, which free Universal SSL does not cover, and roost could not verify ACM coverage (%v)", host, depth, zone.Name, err),
			"grant the token SSL read access to verify, or "+remedy)
	}
	return fail(check,
		fmt.Sprintf("%s is %d levels below zone %s; free Universal SSL covers only ONE subdomain level, so browsers will get an opaque TLS error with nothing in the tunnel or container logs", host, depth, zone.Name),
		remedy)
}
