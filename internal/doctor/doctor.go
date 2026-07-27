// Package doctor runs preflight checks and reports findings with
// specific remedies — never a stack trace.
package doctor

import (
	"fmt"
	"strings"
)

// Level is a finding's severity.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warning"
	Fail Level = "fail"
)

// FixKind identifies a safe, specific remediation doctor can apply for a
// finding when run with --fix. The set is deliberately small: only changes
// that can't clobber something the user meant to keep.
type FixKind string

const (
	// FixProxyDNS flips an existing, correctly-targeted tunnel record to
	// proxied (grey-cloud → orange-cloud). PatchDNSProxied.
	FixProxyDNS FixKind = "proxy-dns"
	// FixCreateDNS creates a wholly missing tunnel record. Never repoints an
	// existing record — a wrong-content record is reported, not overwritten.
	FixCreateDNS FixKind = "create-dns"
	// FixCredPerms chmods the credentials file to 0600.
	FixCredPerms FixKind = "cred-perms"
)

// Fix is the remediation attached to a fixable finding. Params are
// primitives so the doctor core needn't import the Cloudflare client; the
// applier (fix.go) turns them into API/filesystem calls.
type Fix struct {
	Kind     FixKind
	ZoneID   string // proxy-dns, create-dns
	RecordID string // proxy-dns
	Name     string // create-dns / proxy-dns (for the message)
	Content  string // create-dns
	Path     string // cred-perms
}

// Finding is one check's outcome. Fix is non-nil only when --fix can
// safely remediate it.
type Finding struct {
	Check   string
	Level   Level
	Message string
	Remedy  string
	Fix     *Fix
}

func ok(check, message string) Finding {
	return Finding{Check: check, Level: OK, Message: message}
}

func warn(check, message, remedy string) Finding {
	return Finding{Check: check, Level: Warn, Message: message, Remedy: remedy}
}

func fail(check, message, remedy string) Finding {
	return Finding{Check: check, Level: Fail, Message: message, Remedy: remedy}
}

// Summary formats findings for the terminal.
func Summary(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		switch f.Level {
		case OK:
			fmt.Fprintf(&b, "  ok    %s: %s\n", f.Check, f.Message)
		case Warn:
			fmt.Fprintf(&b, "  warn  %s: %s\n", f.Check, f.Message)
		case Fail:
			fmt.Fprintf(&b, "  FAIL  %s: %s\n", f.Check, f.Message)
		}
		if f.Remedy != "" && f.Level != OK {
			fmt.Fprintf(&b, "        fix: %s\n", f.Remedy)
		}
	}
	return b.String()
}

// HasFailures reports whether any finding is a hard failure.
func HasFailures(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == Fail {
			return true
		}
	}
	return false
}
