package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/shell"
)

func newManager(t *testing.T, goos string) (*Manager, *shell.Fake) {
	t.Helper()
	fake := &shell.Fake{}
	return &Manager{
		GOOS:  goos,
		Home:  t.TempDir(),
		Exec:  "/usr/local/bin/roost",
		Shell: fake,
	}, fake
}

func TestEnableDarwin(t *testing.T) {
	m, fake := newManager(t, "darwin")
	path, _, err := m.Enable()
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	want := filepath.Join(m.Home, "Library", "LaunchAgents", "com.roost.plist")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	s := string(data)
	for _, wantSub := range []string{
		"com.roost",
		"/usr/local/bin/roost",
		"<string>up</string>",
		"<key>KeepAlive</key>",
		"<false/>",
		"<key>RunAtLoad</key>",
		".roost/logs/",
	} {
		if !strings.Contains(s, wantSub) {
			t.Errorf("plist missing %q:\n%s", wantSub, s)
		}
	}
	if len(fake.Calls) == 0 || !strings.Contains(allCalls(fake), "launchctl") {
		t.Errorf("Enable should load the agent via launchctl:\n%s", allCalls(fake))
	}
}

func TestEnableLinux(t *testing.T) {
	m, fake := newManager(t, "linux")
	path, notes, err := m.Enable()
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	want := filepath.Join(m.Home, ".config", "systemd", "user", "roost.service")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	s := string(data)
	for _, wantSub := range []string{
		"WantedBy=default.target",
		"/usr/local/bin/roost up",
	} {
		if !strings.Contains(s, wantSub) {
			t.Errorf("unit missing %q:\n%s", wantSub, s)
		}
	}
	if !strings.Contains(strings.Join(notes, "\n"), "loginctl enable-linger") {
		t.Errorf("notes should mention loginctl enable-linger: %v", notes)
	}
	calls := allCalls(fake)
	if !strings.Contains(calls, "daemon-reload") || !strings.Contains(calls, "enable") {
		t.Errorf("Enable should reload and enable via systemctl:\n%s", calls)
	}
}

func TestEnableUnsupportedPlatform(t *testing.T) {
	m, _ := newManager(t, "windows")
	path, notes, err := m.Enable()
	if err != nil {
		t.Fatalf("Enable on unsupported platform should not fail: %v", err)
	}
	if path != "" {
		t.Errorf("no unit should be written on unsupported platforms, got %q", path)
	}
	if len(notes) == 0 {
		t.Error("unsupported platform should print manual instructions")
	}
}

func TestDisableRemovesEverything(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, _ := newManager(t, goos)
			path, _, err := m.Enable()
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Disable(); err != nil {
				t.Fatalf("Disable: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("unit file %s still present after Disable", path)
			}
		})
	}
}

func TestDisableWhenNeverEnabled(t *testing.T) {
	m, _ := newManager(t, "linux")
	if err := m.Disable(); err != nil {
		t.Errorf("Disable with nothing installed should be a no-op, got %v", err)
	}
}

func allCalls(fake *shell.Fake) string {
	var lines []string
	for _, c := range fake.Calls {
		lines = append(lines, c.String())
	}
	return strings.Join(lines, "\n")
}
