package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/runner"
)

// servePost drives a form-encoded POST through the panel's handler.
func servePost(srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// fakeStore is an in-memory SettingsStore for handler tests.
type fakeStore struct {
	saved  Settings
	loaded Settings
	saves  int
}

func (f *fakeStore) Load() (Settings, error) { return f.loaded, nil }
func (f *fakeStore) Save(s Settings) error   { f.saved = s; f.saves++; return nil }

func TestSettingsPageRendersCurrentValues(t *testing.T) {
	f := &fakeStore{loaded: Settings{
		EmailTo: []string{"me@example.com"}, SMTPHost: "smtp.example.com",
		DefaultView: "grid", MaskSensitive: true,
		TechStacks: map[string]string{"rails": "Ruby on Rails"},
	}.Normalize()}
	s := NewServer(&fakeController{}, "")
	s.SetSettingsStore(f)
	body := serve(s, "GET", "/settings", nil).Body.String()
	for _, want := range []string{"me@example.com", "smtp.example.com", "rails=Ruby on Rails", "Mask sensitive", "Save settings", "class=\"rail\""} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	// grid must be the selected option
	if !strings.Contains(body, `value="grid" selected`) {
		t.Error("default view grid should be preselected")
	}
	if !strings.Contains(body, "checked") {
		t.Error("mask checkbox should be checked")
	}
}

func TestSaveSettingsPersistsAndUpdates(t *testing.T) {
	f := &fakeStore{loaded: DefaultSettings()}
	s := NewServer(&fakeController{}, "")
	s.SetSettingsStore(f)

	form := url.Values{
		"emailTo": {"a@x.com, b@x.com"}, "smtpHost": {"smtp.x.com"}, "smtpPort": {"465"},
		"defaultView": {"grid"}, "defaultTheme": {"dark"}, "maskSensitive": {"on"},
		"techStacks": {"rails=Ruby on Rails\nnext=Next.js"}, "monitorMins": {"3"},
		"emailSubject": {"Down: {app}"}, "emailBody": {"{app} {status}"},
	}
	rr := servePost(s, "/settings", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /settings = %d, want 303", rr.Code)
	}
	if f.saves != 1 {
		t.Fatalf("expected one save, got %d", f.saves)
	}
	got := f.saved
	if got.SMTPHost != "smtp.x.com" || got.SMTPPort != 465 || got.DefaultView != "grid" || got.DefaultTheme != "dark" || !got.MaskSensitive || got.MonitorMins != 3 {
		t.Errorf("saved settings mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.EmailTo, []string{"a@x.com", "b@x.com"}) {
		t.Errorf("EmailTo = %#v", got.EmailTo)
	}
	if got.TechStacks["rails"] != "Ruby on Rails" || got.TechStacks["next"] != "Next.js" {
		t.Errorf("TechStacks = %#v", got.TechStacks)
	}
	// live settings updated too
	if s.currentSettings().SMTPHost != "smtp.x.com" {
		t.Error("live settings not updated after save")
	}
}

func TestSaveSettingsRebuildsNotifier(t *testing.T) {
	s := NewServer(&fakeController{}, "")
	s.SetSettingsStore(&fakeStore{loaded: DefaultSettings()})
	var gotHost string
	s.SetMailerFactory(func(cfg Settings) Notifier {
		gotHost = cfg.SMTPHost
		return nil // disabled — fine for this test
	})
	servePost(s, "/settings", url.Values{"smtpHost": {"smtp.rebuilt.com"}, "monitorMins": {"2"}})
	if gotHost != "smtp.rebuilt.com" {
		t.Errorf("mailer factory not rebuilt with new host, got %q", gotHost)
	}
}

func TestDashboardMaskAndTechOverride(t *testing.T) {
	f := &fakeController{
		statuses: []runner.AppStatus{{Name: "crm", State: "running", URL: "https://crm.example.com", Framework: "rails", Database: "postgres"}},
		server:   ServerInfo{IP: "203.0.113.9", SSH: "ubuntu@203.0.113.9", Host: "box-01"},
	}
	s := NewServer(f, "")
	s.SetSettingsStore(&fakeStore{loaded: Settings{
		MaskSensitive: true,
		TechStacks:    map[string]string{"rails": "Ruby on Rails"},
	}.Normalize()})

	body := serve(s, "GET", "/", nil).Body.String()
	if strings.Contains(body, "203.0.113.9") {
		t.Error("mask mode must not leak the raw IP")
	}
	if !strings.Contains(body, "hidden") {
		t.Error("mask mode should show a masked placeholder")
	}
	if strings.Contains(body, "ubuntu@203.0.113.9") {
		t.Error("mask mode must hide the SSH login command")
	}
	if !strings.Contains(body, "Ruby on Rails") {
		t.Error("tech-stack override label should render on the card")
	}
}

func TestIncidentSummary(t *testing.T) {
	// all clear
	if got := incidentSummary(statusView{RunningCount: 5, Total: 5}); !strings.Contains(got, "operational") || !strings.Contains(got, "5/5") {
		t.Errorf("all-clear summary = %q", got)
	}
	// with open incidents
	v := statusView{
		OpenIncidents: 2,
		Incidents: []incidentView{
			{Label: "Keeparu", Ago: "down 6m", Open: true},
			{Label: "Crm", Ago: "down 23m", Open: true},
			{Label: "Old", Ago: "resolved after 3m", Open: false},
		},
	}
	got := incidentSummary(v)
	if !strings.Contains(got, "2 services affected") || !strings.Contains(got, "Keeparu (down 6m)") || !strings.Contains(got, "Crm (down 23m)") {
		t.Errorf("open-incident summary = %q", got)
	}
	if strings.Contains(got, "Old") {
		t.Error("resolved incidents should not appear in the share summary")
	}
	// singular
	if got := incidentSummary(statusView{OpenIncidents: 1, Incidents: []incidentView{{Label: "X", Ago: "down 1m", Open: true}}}); !strings.Contains(got, "1 service affected") {
		t.Errorf("singular summary = %q", got)
	}
}

func TestStatusPageShowsIncidentDetailAndRefreshes(t *testing.T) {
	f := &fakeController{}
	s := NewServer(f, "")
	f.statuses = []runner.AppStatus{{Name: "keeparu", State: "running", URL: "https://keeparu.example.com", HTTP: "200", Reachable: true}}
	s.checkIncidents()
	f.statuses = []runner.AppStatus{{Name: "keeparu", State: "exited", URL: "https://keeparu.example.com"}}
	s.checkIncidents() // open incident with detail "exited"

	body := serve(s, "GET", "/status", nil).Body.String()
	if !strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Error("status page should auto-refresh")
	}
	if !strings.Contains(body, "exited") {
		t.Error("status page should surface the incident detail")
	}
}

func TestSettingsNormalizeFillsDefaults(t *testing.T) {
	got := Settings{DefaultView: "weird", DefaultTheme: "neon", EmailTo: []string{" a@x.com ", "", "b@x.com"}}.Normalize()
	if got.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want 587 default", got.SMTPPort)
	}
	if got.DefaultView != "list" {
		t.Errorf("invalid view should fall back to list, got %q", got.DefaultView)
	}
	if got.DefaultTheme != "system" {
		t.Errorf("invalid theme should fall back to system, got %q", got.DefaultTheme)
	}
	if got.MonitorMins != 2 {
		t.Errorf("MonitorMins default = %d, want 2", got.MonitorMins)
	}
	if !reflect.DeepEqual(got.EmailTo, []string{"a@x.com", "b@x.com"}) {
		t.Errorf("EmailTo should trim + drop blanks, got %#v", got.EmailTo)
	}
	if got.EmailSubject == "" || got.EmailBody == "" {
		t.Error("blank email templates should fall back to defaults")
	}
}

func TestSettingsNormalizeKeepsValidGrid(t *testing.T) {
	if got := (Settings{DefaultView: "grid", DefaultTheme: "dark", MonitorMins: 5}).Normalize(); got.DefaultView != "grid" || got.DefaultTheme != "dark" || got.MonitorMins != 5 {
		t.Errorf("valid values must survive normalize: %+v", got)
	}
}

func TestRenderEmailExpandsPlaceholders(t *testing.T) {
	s := Settings{
		EmailSubject: "Roost · {app} is {status}",
		EmailBody:    "{app} is {detail}.\n{url}\n{time}",
	}
	sub, body := s.renderEmail("Keeparu", "down", "exited", "https://keeparu.example.com", "Mon 10:00")
	if sub != "Roost · Keeparu is down" {
		t.Errorf("subject = %q", sub)
	}
	if !strings.Contains(body, "Keeparu is exited.") || !strings.Contains(body, "https://keeparu.example.com") || !strings.Contains(body, "Mon 10:00") {
		t.Errorf("body did not expand placeholders: %q", body)
	}
}

func TestTechLabelOverride(t *testing.T) {
	s := Settings{TechStacks: map[string]string{"rails": "Ruby on Rails", "blank": ""}}
	if got := s.TechLabel("rails"); got != "Ruby on Rails" {
		t.Errorf("override not applied: %q", got)
	}
	// Empty override falls back to the built-in label, not "".
	if got := s.TechLabel("next"); got == "" {
		t.Errorf("unmapped key should fall back to tech(), got empty")
	}
	if got := s.TechLabel("blank"); got == "" {
		t.Errorf("blank override should fall back to tech(), got empty")
	}
	if got := (Settings{}).TechLabel(""); got != "" {
		t.Errorf("empty key should render empty, got %q", got)
	}
}
