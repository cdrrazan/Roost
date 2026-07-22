package detect

import "testing"

func TestDetectRedis(t *testing.T) {
	sidekiq := writeFiles(t, map[string]string{
		"Gemfile":               "source \"https://rubygems.org\"\ngem \"rails\"\ngem \"pg\"\ngem \"sidekiq\"\n",
		"config/application.rb": "module App; end\n",
		"config/database.yml":   "default:\n  adapter: postgresql\n",
	})
	got, err := Detect(sidekiq)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !got.Redis {
		t.Error("Redis = false, want true (sidekiq gem)")
	}
	if got.Database != "postgres" {
		t.Errorf("Database = %q, want postgres", got.Database)
	}

	envRedis := writeFiles(t, map[string]string{
		"Gemfile":               "source \"x\"\ngem \"rails\"\ngem \"pg\"\n",
		"config/application.rb": "module App; end\n",
		".env.example":          "SECRET=1\nREDIS_URL=redis://localhost:6379/1\n",
	})
	got, err = Detect(envRedis)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !got.Redis {
		t.Error("Redis = false, want true (REDIS_URL in .env.example)")
	}

	none := writeFiles(t, map[string]string{
		"Gemfile":               "source \"x\"\ngem \"rails\"\ngem \"pg\"\n",
		"config/application.rb": "module App; end\n",
	})
	got, err = Detect(none)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got.Redis {
		t.Error("Redis = true, want false (no redis signal)")
	}
}
