package doctor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cdrrazan/roost/internal/shell"
)

func TestCheckDocker(t *testing.T) {
	t.Run("missing binary fails with install remedy", func(t *testing.T) {
		fake := &shell.Fake{RunFunc: func(name string, args ...string) (shell.Result, error) {
			return shell.Result{}, fmt.Errorf("exec: not found")
		}}
		fs := CheckDocker(fake)
		if len(fs) != 1 || fs[0].Level != Fail || !strings.Contains(fs[0].Remedy, "docker.com") {
			t.Errorf("findings = %+v", fs)
		}
	})

	t.Run("daemon down is a distinct failure", func(t *testing.T) {
		fake := &shell.Fake{RunFunc: func(name string, args ...string) (shell.Result, error) {
			if args[0] == "info" {
				return shell.Result{}, fmt.Errorf("cannot connect to the Docker daemon")
			}
			return shell.Result{}, nil
		}}
		fs := CheckDocker(fake)
		if len(fs) != 1 || fs[0].Level != Fail || !strings.Contains(fs[0].Message, "not running") {
			t.Errorf("findings = %+v", fs)
		}
	})

	t.Run("healthy passes", func(t *testing.T) {
		fs := CheckDocker(&shell.Fake{})
		if len(fs) != 1 || fs[0].Level != OK {
			t.Errorf("findings = %+v", fs)
		}
	})
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"512m", 512 << 20},
		{"1g", 1 << 30},
		{"768M", 768 << 20},
		{"1024k", 1 << 20},
		{"42", 42},
	}
	for _, tt := range tests {
		got, err := ParseMemory(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
	if _, err := ParseMemory("lots"); err == nil {
		t.Error("want error for garbage")
	}
}

func TestCompareMemoryBudget(t *testing.T) {
	caps := map[string]string{"a": "512m", "b": "512m"}

	if f := CompareMemoryBudget(caps, 8<<30); f.Level != OK {
		t.Errorf("finding = %+v, want OK within budget", f)
	}
	f := CompareMemoryBudget(caps, 512<<20)
	if f.Level != Fail {
		t.Errorf("finding = %+v, want Fail over budget", f)
	}
	if !strings.Contains(f.Remedy, "profile") {
		t.Errorf("remedy should suggest profiles: %+v", f)
	}
}
