// Package detect inspects an application directory and infers its
// framework, port, start command, database need, and runtime version.
// Detection is explainable: every result names the signal that
// triggered it, and failure to detect is an explicit error, never a
// silent guess.
package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Detection is the result of inspecting one app directory.
type Detection struct {
	// Framework is one of: rails, sinatra, next, node, django, static.
	Framework string
	// Signal is the human-readable rule that triggered the detection,
	// e.g. "Gemfile + config/application.rb".
	Signal string
	// Port is the framework's default port inside the container.
	Port int
	// StartCommand is the framework default start command, binding to
	// 0.0.0.0 explicitly. Empty for static sites (Caddy serves them).
	StartCommand string
	// Database is "mysql", "postgres", or "" when no database need was
	// detected.
	Database string
	// RuntimeVersion is the language runtime version inferred from
	// .ruby-version, the Gemfile ruby line, .node-version, or
	// package.json engines. Empty when nothing declares one.
	RuntimeVersion string
}

// packageJSON is the subset of package.json detection cares about.
type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func (p *packageJSON) hasDep(name string) bool {
	if p == nil {
		return false
	}
	_, inDeps := p.Dependencies[name]
	_, inDev := p.DevDependencies[name]
	return inDeps || inDev
}

// Detect inspects dir and returns what it found. It returns an error
// naming the directory when no rule matches, telling the user to set
// framework: explicitly.
func Detect(dir string) (Detection, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return Detection{}, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !info.IsDir() {
		return Detection{}, fmt.Errorf("inspect %s: not a directory", dir)
	}

	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return ""
		}
		return string(data)
	}

	var pkg *packageJSON
	if has("package.json") {
		var p packageJSON
		if err := json.Unmarshal([]byte(read("package.json")), &p); err == nil {
			pkg = &p
		}
	}

	// Rules in priority order.
	switch {
	case has("Gemfile") && has("config/application.rb"):
		return Detection{
			Framework:      "rails",
			Signal:         "Gemfile + config/application.rb",
			Port:           3000,
			StartCommand:   "bundle exec puma -b tcp://0.0.0.0:3000",
			Database:       detectDatabase(dir, read),
			RuntimeVersion: rubyVersion(read),
		}, nil

	case has("Gemfile") && has("config.ru") && strings.Contains(read("Gemfile"), "sinatra"):
		return Detection{
			Framework:      "sinatra",
			Signal:         "Gemfile + config.ru + sinatra in Gemfile",
			Port:           4567,
			StartCommand:   "bundle exec rackup -o 0.0.0.0 -p 4567",
			Database:       detectDatabase(dir, read),
			RuntimeVersion: rubyVersion(read),
		}, nil

	case pkg.hasDep("next"):
		return Detection{
			Framework:      "next",
			Signal:         "package.json with next dependency",
			Port:           3000,
			StartCommand:   "npm run start",
			Database:       detectDatabase(dir, read),
			RuntimeVersion: nodeVersion(read, pkg),
		}, nil

	case pkg.hasDep("vite"):
		return Detection{
			Framework:      "static",
			Signal:         "package.json with vite dependency",
			Port:           80,
			Database:       detectDatabase(dir, read),
			RuntimeVersion: nodeVersion(read, pkg),
		}, nil

	case pkg.hasDep("express"):
		return Detection{
			Framework:      "node",
			Signal:         "package.json with express dependency",
			Port:           3000,
			StartCommand:   "npm run start",
			Database:       detectDatabase(dir, read),
			RuntimeVersion: nodeVersion(read, pkg),
		}, nil

	case has("manage.py") && (has("requirements.txt") || has("pyproject.toml")):
		signal := "manage.py + requirements.txt"
		if !has("requirements.txt") {
			signal = "manage.py + pyproject.toml"
		}
		return Detection{
			Framework:    "django",
			Signal:       signal,
			Port:         8000,
			StartCommand: "gunicorn -b 0.0.0.0:8000",
			Database:     detectDatabase(dir, read),
		}, nil

	case has("index.html"):
		return Detection{
			Framework: "static",
			Signal:    "index.html at root",
			Port:      80,
		}, nil
	}

	return Detection{}, fmt.Errorf("%s: could not detect a framework; set framework: explicitly for this app in config.yml", filepath.Base(dir))
}

// detectDatabase infers the database an app needs, checking the most
// authoritative source first: the Rails database.yml adapter, then
// DATABASE_URL in .env.example, then database gems in the Gemfile.
func detectDatabase(dir string, read func(string) string) string {
	if dbYML := read("config/database.yml"); dbYML != "" {
		if db := classifyDatabase(adapterLine(dbYML)); db != "" {
			return db
		}
	}
	for _, envFile := range []string{".env.example", ".env.sample"} {
		for _, line := range strings.Split(read(envFile), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "DATABASE_URL=") {
				if db := classifyDatabase(line); db != "" {
					return db
				}
			}
		}
	}
	if gemfile := read("Gemfile"); gemfile != "" {
		if strings.Contains(gemfile, `"mysql2"`) || strings.Contains(gemfile, `'mysql2'`) {
			return "mysql"
		}
		if strings.Contains(gemfile, `"pg"`) || strings.Contains(gemfile, `'pg'`) {
			return "postgres"
		}
	}
	return ""
}

// adapterLine returns the first adapter: line from a database.yml.
func adapterLine(dbYML string) string {
	for _, line := range strings.Split(dbYML, "\n") {
		if strings.Contains(line, "adapter:") {
			return line
		}
	}
	return ""
}

// classifyDatabase maps a string mentioning a database to roost's
// database identifiers.
func classifyDatabase(s string) string {
	switch {
	case strings.Contains(s, "mysql"):
		return "mysql"
	case strings.Contains(s, "postgres"): // covers postgresql too
		return "postgres"
	default:
		return ""
	}
}

var gemfileRubyLine = regexp.MustCompile(`(?m)^\s*ruby\s+["']([^"']+)["']`)

// rubyVersion reads the Ruby version from .ruby-version or the
// Gemfile's ruby line.
func rubyVersion(read func(string) string) string {
	if v := strings.TrimSpace(read(".ruby-version")); v != "" {
		return v
	}
	if m := gemfileRubyLine.FindStringSubmatch(read("Gemfile")); m != nil {
		return m[1]
	}
	return ""
}

// nodeVersion reads the Node version from .node-version, .nvmrc, or
// package.json engines.
func nodeVersion(read func(string) string, pkg *packageJSON) string {
	if v := strings.TrimSpace(read(".node-version")); v != "" {
		return v
	}
	if v := strings.TrimSpace(read(".nvmrc")); v != "" {
		return v
	}
	if pkg != nil {
		return pkg.Engines.Node
	}
	return ""
}
