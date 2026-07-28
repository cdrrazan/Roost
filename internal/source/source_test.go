package source

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cdrrazan/roost/internal/shell"
)

func TestClone(t *testing.T) {
	t.Run("runs git clone into a fresh dest", func(t *testing.T) {
		f := &shell.Fake{}
		dest := filepath.Join(t.TempDir(), "sources", "app")
		if err := Clone(f, "https://github.com/u/app", dest); err != nil {
			t.Fatalf("Clone: %v", err)
		}
		if len(f.Calls) != 1 {
			t.Fatalf("calls = %d, want 1", len(f.Calls))
		}
		got := f.Calls[0].String()
		want := "git clone https://github.com/u/app " + dest
		if got != want {
			t.Errorf("call = %q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Dir(dest)); err != nil {
			t.Errorf("parent dir not created: %v", err)
		}
	})

	t.Run("refuses an existing destination", func(t *testing.T) {
		f := &shell.Fake{}
		dest := t.TempDir() // already exists
		if err := Clone(f, "https://github.com/u/app", dest); err == nil {
			t.Fatal("expected error for existing dest")
		}
		if len(f.Calls) != 0 {
			t.Errorf("git ran despite existing dest: %v", f.Calls)
		}
	})

	t.Run("empty repo is an error", func(t *testing.T) {
		if err := Clone(&shell.Fake{}, "", filepath.Join(t.TempDir(), "x")); err == nil {
			t.Fatal("expected error for empty repo")
		}
	})
}

func TestNameFromRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/u/Fizzy.git": "fizzy",
		"https://github.com/u/my-app":    "my-app",
		"git@github.com:u/Some_App.git":  "some-app",
		"https://github.com/u/app/":      "app",
	}
	for in, want := range cases {
		if got := NameFromRepo(in); got != want {
			t.Errorf("NameFromRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
