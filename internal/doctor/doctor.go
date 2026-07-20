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

// Finding is one check's outcome.
type Finding struct {
	Check   string
	Level   Level
	Message string
	Remedy  string
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
