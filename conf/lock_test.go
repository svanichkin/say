package conf

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The lock is per process (flock and LockFileEx both are), so the second
// Say is played by a child process running this test binary.
func TestSecondSayWithTheSameConfigIsRefused(t *testing.T) {
	if os.Getenv("SAY_LOCK_CHILD") != "" {
		if _, err := LockConfig(os.Getenv("SAY_LOCK_CHILD")); err != nil {
			os.Stdout.WriteString("refused: " + err.Error())
			return
		}
		os.Stdout.WriteString("taken")
		return
	}
	cfg := filepath.Join(t.TempDir(), "config.json")
	second := func() string {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSecondSayWithTheSameConfigIsRefused$")
		cmd.Env = append(os.Environ(), "SAY_LOCK_CHILD="+cfg)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child: %v", err)
		}
		return string(out)
	}

	release, err := LockConfig(cfg)
	if err != nil {
		t.Fatalf("the first Say: %v", err)
	}
	if out := second(); !strings.Contains(out, "refused: another Say is already running") {
		t.Fatalf("a second Say with the same configuration was not refused: %q", out)
	}
	release()
	if out := second(); !strings.Contains(out, "taken") {
		t.Fatalf("the configuration stayed locked after the first Say let it go: %q", out)
	}
	other, err := LockConfig(filepath.Join(filepath.Dir(cfg), "other.json"))
	if err != nil {
		t.Fatalf("another configuration was refused: %v", err)
	}
	other()
}
