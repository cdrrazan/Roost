package doctor

import (
	"os"

	"github.com/cdrrazan/roost/internal/tunnel"
)

// ApplyFixes runs the remediation attached to each fixable finding and
// returns one result finding per attempt (findings without a Fix are
// skipped). DNS fixes need client; cred-perms only touches the filesystem,
// so a nil client is tolerated — the credentials fix still applies and DNS
// fixes report that a valid token is needed first.
func ApplyFixes(findings []Finding, client *tunnel.Client) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
		out = append(out, applyFix(f, client))
	}
	return out
}

func applyFix(f Finding, client *tunnel.Client) Finding {
	check := "fix:" + f.Check
	fx := f.Fix
	switch fx.Kind {
	case FixCredPerms:
		if err := os.Chmod(fx.Path, 0o600); err != nil {
			return fail(check, "chmod failed: "+err.Error(), "chmod 600 "+fx.Path+" by hand")
		}
		return ok(check, "chmod 600 "+fx.Path)

	case FixProxyDNS:
		if client == nil {
			return fail(check, "need a valid API token to fix DNS", "fix credentials, then re-run `roost doctor --fix`")
		}
		if err := client.PatchDNSProxied(fx.ZoneID, fx.RecordID); err != nil {
			return fail(check, err.Error(), "run `roost tunnel setup`")
		}
		return ok(check, "set "+fx.Name+" to proxied")

	case FixCreateDNS:
		if client == nil {
			return fail(check, "need a valid API token to fix DNS", "fix credentials, then re-run `roost doctor --fix`")
		}
		_, err := client.CreateDNS(fx.ZoneID, tunnel.DNSRecord{
			Type: "CNAME", Name: fx.Name, Content: fx.Content, Proxied: true,
		})
		if err != nil {
			return fail(check, err.Error(), "run `roost tunnel setup`")
		}
		return ok(check, "created "+fx.Name+" → "+fx.Content)

	default:
		return fail(check, "unknown fix kind "+string(fx.Kind), "")
	}
}
