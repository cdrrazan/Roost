package web

import "strings"

// Settings holds panel-level preferences persisted to ~/.roost/panel.json.
//
// It NEVER holds the SMTP password: delivery credentials stay in
// $ROOST_SMTP_PASSWORD only, so a panel.json that leaks (backup, screen-share)
// can't send mail as the user. The file carries recipient/host/template and UI
// preferences — annoying to lose, not dangerous.
type Settings struct {
	// Incident email (password is $ROOST_SMTP_PASSWORD only, never here).
	EmailTo  []string `json:"emailTo"`
	SMTPHost string   `json:"smtpHost"`
	SMTPPort int      `json:"smtpPort"`
	SMTPUser string   `json:"smtpUser"`
	SMTPFrom string   `json:"smtpFrom"`
	// EmailSubject/EmailBody are templates with {app} {status} {detail} {url}
	// {time} placeholders (see renderEmail).
	EmailSubject string `json:"emailSubject"`
	EmailBody    string `json:"emailBody"`

	// Dashboard preferences.
	DefaultView   string            `json:"defaultView"`   // "list" | "grid"
	DefaultTheme  string            `json:"defaultTheme"`  // "light" | "dark" | "system"
	MaskSensitive bool              `json:"maskSensitive"` // hide IP/SSH/host on the private cards
	TechStacks    map[string]string `json:"techStacks"`    // framework/db key -> display label override

	// MonitorMins is the incident-detection interval in minutes.
	MonitorMins int `json:"monitorMins"`
}

// DefaultSettings returns the out-of-the-box configuration. Every zero-valued
// field a user leaves blank falls back to one of these via Normalize.
func DefaultSettings() Settings {
	return Settings{
		SMTPPort:      587,
		EmailSubject:  "Roost · {app} is {status}",
		EmailBody:     "{app} is {detail}.\n\n{url}\n{time}",
		DefaultView:   "list",
		DefaultTheme:  "system",
		MaskSensitive: false,
		MonitorMins:   2,
	}
}

// Normalize fills blank/invalid fields with defaults so the rest of the panel
// can trust the values without re-checking. Idempotent.
func (s Settings) Normalize() Settings {
	d := DefaultSettings()
	if s.SMTPPort <= 0 {
		s.SMTPPort = d.SMTPPort
	}
	if strings.TrimSpace(s.EmailSubject) == "" {
		s.EmailSubject = d.EmailSubject
	}
	if strings.TrimSpace(s.EmailBody) == "" {
		s.EmailBody = d.EmailBody
	}
	if s.DefaultView != "grid" {
		s.DefaultView = "list"
	}
	switch s.DefaultTheme {
	case "light", "dark", "system":
	default:
		s.DefaultTheme = d.DefaultTheme
	}
	if s.MonitorMins < 1 {
		s.MonitorMins = d.MonitorMins
	}
	// Trim recipient noise so an accidental trailing comma isn't a phantom
	// empty address the SMTP server rejects.
	var to []string
	for _, addr := range s.EmailTo {
		if a := strings.TrimSpace(addr); a != "" {
			to = append(to, a)
		}
	}
	s.EmailTo = to
	return s
}

// TechLabel resolves a framework/db key to its display label, honoring the
// user's TechStacks overrides before falling back to the built-in tech() map.
// Exported so the dashboard template can call it per app.
func (s Settings) TechLabel(key string) string {
	if key == "" {
		return ""
	}
	if s.TechStacks != nil {
		if label, ok := s.TechStacks[key]; ok && strings.TrimSpace(label) != "" {
			return label
		}
	}
	return tech(key)
}

// renderEmail expands the subject/body templates for one incident. Unknown
// placeholders are left untouched; missing values render empty.
func (s Settings) renderEmail(app, status, detail, url, at string) (subject, body string) {
	r := strings.NewReplacer(
		"{app}", app,
		"{status}", status,
		"{detail}", detail,
		"{url}", url,
		"{time}", at,
	)
	return r.Replace(s.EmailSubject), r.Replace(s.EmailBody)
}

// maskValue hides a sensitive value (IP, tunnel id, account) when mask mode is
// on, so the private cards are safe to show on a screen-share. A blank value
// stays blank so the template's "is it set?" checks still work.
func maskValue(on bool, v string) string {
	if !on || v == "" {
		return v
	}
	return "•• hidden ••"
}

// SettingsStore loads and persists panel settings. A nil store means the panel
// runs on DefaultSettings with no persistence (the default in tests).
type SettingsStore interface {
	Load() (Settings, error)
	Save(Settings) error
}
