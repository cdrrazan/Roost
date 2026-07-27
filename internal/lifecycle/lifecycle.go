// Package lifecycle installs and removes the boot-on-login unit: launchd on
// macOS, a systemd user unit on Linux, a Task Scheduler task on Windows. All
// run `roost up` at login; Docker's restart policy handles everything after.
package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cdrrazan/roost/internal/shell"
)

// Manager installs or removes the platform unit.
type Manager struct {
	GOOS  string // runtime.GOOS, injectable for tests
	Home  string
	Exec  string // absolute path to the roost binary
	Shell shell.Runner
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.roost</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>up</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`

const systemdTemplate = `[Unit]
Description=roost — bring local apps up at login

[Service]
Type=oneshot
ExecStart=%s up
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`

func (m *Manager) unitPath() string {
	switch m.GOOS {
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", "com.roost.plist")
	case "linux":
		return filepath.Join(m.Home, ".config", "systemd", "user", "roost.service")
	default:
		return ""
	}
}

func (m *Manager) logPath(name string) string {
	return filepath.Join(m.Home, ".roost", "logs", name)
}

// windowsTask is the Task Scheduler task name.
const windowsTask = "roost"

// Enable writes the platform unit and activates it. On unsupported
// platforms it returns manual instructions instead of failing.
func (m *Manager) Enable() (path string, notes []string, err error) {
	if m.GOOS == "windows" {
		// Windows has no unit file: schtasks registers a logon task directly.
		// /SC ONLOGON runs at each logon; /F overwrites a prior task.
		if _, err := m.Shell.Run("schtasks", "/Create", "/SC", "ONLOGON", "/TN", windowsTask, "/TR", m.Exec+" up", "/F"); err != nil {
			return "", nil, fmt.Errorf("create scheduled task: %w", err)
		}
		return `Task Scheduler\` + windowsTask, []string{
			`Registered the Task Scheduler task "` + windowsTask + `" — runs "` + m.Exec + ` up" at each logon.`,
		}, nil
	}
	path = m.unitPath()
	if path == "" {
		return "", []string{
			fmt.Sprintf("no boot-on-login integration for %s;", m.GOOS),
			fmt.Sprintf("arrange for `%s up` to run at login with your platform's scheduler.", m.Exec),
		}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(m.Home, ".roost", "logs"), 0o755); err != nil {
		return "", nil, fmt.Errorf("create log directory: %w", err)
	}

	var unit string
	switch m.GOOS {
	case "darwin":
		unit = fmt.Sprintf(plistTemplate, m.Exec, m.logPath("roost.log"), m.logPath("roost.err.log"))
	case "linux":
		unit = fmt.Sprintf(systemdTemplate, m.Exec, m.logPath("roost.log"), m.logPath("roost.err.log"))
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return "", nil, fmt.Errorf("write unit %s: %w", path, err)
	}

	switch m.GOOS {
	case "darwin":
		// Reload cleanly if a previous version was loaded.
		_, _ = m.Shell.Run("launchctl", "unload", path)
		if _, err := m.Shell.Run("launchctl", "load", path); err != nil {
			return "", nil, fmt.Errorf("load launch agent: %w", err)
		}
	case "linux":
		if _, err := m.Shell.Run("systemctl", "--user", "daemon-reload"); err != nil {
			return "", nil, fmt.Errorf("systemctl daemon-reload: %w", err)
		}
		if _, err := m.Shell.Run("systemctl", "--user", "enable", "roost.service"); err != nil {
			return "", nil, fmt.Errorf("systemctl enable: %w", err)
		}
		notes = append(notes,
			"To start at boot without an active session, run: loginctl enable-linger "+os.Getenv("USER"),
		)
	}
	return path, notes, nil
}

// Disable deactivates and removes the unit, leaving nothing behind.
// It is a no-op when nothing was installed.
func (m *Manager) Disable() error {
	if m.GOOS == "windows" {
		// /F suppresses the confirm prompt; a missing task is not an error we
		// want to surface (Disable is a no-op when nothing was installed).
		_, _ = m.Shell.Run("schtasks", "/Delete", "/TN", windowsTask, "/F")
		return nil
	}
	path := m.unitPath()
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	switch m.GOOS {
	case "darwin":
		_, _ = m.Shell.Run("launchctl", "unload", path)
	case "linux":
		_, _ = m.Shell.Run("systemctl", "--user", "disable", "roost.service")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit %s: %w", path, err)
	}
	return nil
}
