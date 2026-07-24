package generate

import (
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/config"
)

func TestPlanRedisCommandWorker(t *testing.T) {
	cfg := &config.Config{}
	resolved := []config.ResolvedApp{
		{App: config.App{Framework: "rails", Database: "postgres", Redis: config.RedisSpec{Set: true, Enabled: true}}, Name: "web", FQDN: "web.example.com"},
		{App: config.App{Framework: "rails", Redis: config.RedisSpec{Set: true, Enabled: true}, Worker: true, Command: "bundle exec sidekiq"}, Name: "worker", FQDN: ""},
		// redis: false forces it off.
		{App: config.App{Framework: "rails", Database: "postgres", Redis: config.RedisSpec{Set: true, Enabled: false}}, Name: "off", FQDN: "off.example.com"},
	}
	apps, err := Plan(cfg, resolved)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	by := map[string]App{}
	for _, a := range apps {
		by[a.Name] = a
	}

	if !by["web"].Redis {
		t.Error("web Redis = false, want true")
	}
	if by["off"].Redis {
		t.Error("off Redis = true, want false (redis: false)")
	}

	w := by["worker"]
	if w.StartCommand != "bundle exec sidekiq" {
		t.Errorf("worker StartCommand = %q, want the command override", w.StartCommand)
	}
	// Command drives the compose-level override for own-Dockerfile apps; it
	// must be populated too, not just StartCommand.
	if w.Command != "bundle exec sidekiq" {
		t.Errorf("worker Command = %q, want the compose command override", w.Command)
	}
	if !w.Worker {
		t.Error("worker.Worker = false, want true")
	}
	if w.SetupCommand != "" || w.SeedCommand != "" {
		t.Errorf("worker got setup/seed (%q / %q); a worker owns no DB lifecycle", w.SetupCommand, w.SeedCommand)
	}
}

func TestRenderComposeRedisAndCommand(t *testing.T) {
	apps := []App{
		{Name: "web", FQDN: "web.example.com", Framework: "rails", Port: 3000, Database: "postgres", Redis: true, Memory: "512m"},
		{Name: "worker", Framework: "rails", Port: 3000, Redis: true, Command: "bundle exec sidekiq", Worker: true, Memory: "512m",
			Env: map[string]string{"DATABASE_URL": "postgres://roost:roost@postgres:5432/web"}},
	}
	out, err := RenderCompose("/b", apps, "")
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "image: redis:7-alpine") {
		t.Error("compose missing redis service")
	}
	if !strings.Contains(s, "roost-redis-data:") {
		t.Error("compose missing redis volume")
	}
	if !strings.Contains(s, "command: bundle exec sidekiq") {
		t.Error("compose missing worker command override")
	}
	if !strings.Contains(s, `REDIS_URL: "redis://redis:6379/0"`) {
		t.Errorf("compose missing injected REDIS_URL:\n%s", s)
	}
	if !strings.Contains(s, "- postgres") || !strings.Contains(s, "- redis") {
		t.Errorf("web missing depends_on postgres/redis:\n%s", s)
	}
}

func TestRenderCaddyfileExcludesWorker(t *testing.T) {
	apps := []App{
		{Name: "web", FQDN: "web.example.com", Framework: "rails", Port: 3000},
		{Name: "worker", Framework: "rails", Port: 3000, Worker: true, Command: "bundle exec sidekiq"},
	}
	out, err := RenderCaddyfile(apps, "")
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "web.example.com") {
		t.Error("caddyfile missing the web route")
	}
	if strings.Contains(s, "worker") {
		t.Errorf("caddyfile must not route a worker:\n%s", s)
	}
}
