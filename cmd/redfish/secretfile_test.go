package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skipNoSymlink(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows, and O_NOFOLLOW does not exist there")
	}
}

// A symlink planted at a fixed temporary name must not receive the secret.
func TestWriteSecretIgnoresAPlantedSymlink(t *testing.T) {
	skipNoSymlink(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "s.json")
	for _, name := range []string{p + ".new", p + ".tmp", p + ".0000.tmp"} {
		if err := os.Symlink(victim, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeSecret(p, []byte("TOKEN")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "old" {
		t.Errorf("the secret went through the symlink: victim = %q", b)
	}
	li, err := os.Lstat(p)
	if err != nil || !li.Mode().IsRegular() || li.Mode().Perm() != 0o600 {
		t.Fatalf("the destination is not a 0600 regular file: %v %v", li, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "TOKEN" {
		t.Errorf("destination = %q", b)
	}
}

// A symlink at the destination is replaced, not written through.
func TestWriteSecretReplacesASymlinkDestination(t *testing.T) {
	skipNoSymlink(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "s.json")
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}
	if err := writeSecret(p, []byte("TOKEN")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "old" {
		t.Errorf("victim = %q", b)
	}
	if li, _ := os.Lstat(p); !li.Mode().IsRegular() {
		t.Errorf("the destination is still a symlink")
	}
}

func TestWriteSecretLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	for i := 0; i < 3; i++ {
		if err := writeSecret(p, []byte("TOKEN")); err != nil {
			t.Fatal(err)
		}
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 {
		t.Errorf("%d entries in the directory, want only the secret", len(es))
	}
	if err := writeSecret(filepath.Join(dir, "no", "such", "s.json"), []byte("x")); err == nil {
		t.Error("a write into a missing directory succeeded")
	}
}

// A symlink as the password file or the session file is refused, whatever it
// points to (the mode check would read the target, not the link).
func TestSecretFilesRefuseASymlink(t *testing.T) {
	skipNoSymlink(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (filePassword{"u", link}).Get(context.Background()); err == nil {
		t.Error("a symlinked password file was read")
	}
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	g := &globals{endpoint: "https://bmc.example"}
	p, _ := g.sessionFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	good := `{"endpoint":"https://bmc.example","token":"t","uri":"/redfish/v1/S/1"}`
	if err := os.WriteFile(target, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, err := g.loadSession(); err == nil {
		t.Error("a symlinked session file was read")
	}
}

func TestSecretFilesRefuseAFifo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mkfifo")
	}
	skipNoSymlink(t)
	p := filepath.Join(t.TempDir(), "pw")
	if err := mkfifo(p); err != nil {
		t.Skip("mkfifo:", err)
	}
	_, _, err := (filePassword{"u", p}).Get(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a FIFO as the password file: %v", err)
	}
}

func TestSessionFileIsBounded(t *testing.T) {
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	g := &globals{endpoint: "https://bmc.example"}
	p, _ := g.sessionFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("a", maxSessionFile)
	doc := `{"endpoint":"https://bmc.example","token":"t","uri":"/redfish/v1/S/1","pad":"` + pad + `"}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := g.loadSession(); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an oversized session file: %v", err)
	}
	ok := `{"endpoint":"https://bmc.example","token":"t","uri":"/redfish/v1/S/1"}`
	if err := os.WriteFile(p, []byte(ok), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := g.loadSession(); err != nil || s.Token != "t" {
		t.Errorf("a good session file: %v %v", s, err)
	}
}

// readSecret holds the bound itself: a file that grew after the Stat is read
// no further than the cap.
func TestReadSecretCapsWhatGrewAfterTheStat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := readSecret(f, "session file", p, 10); err == nil {
		t.Error("100 bytes read under a cap of 10")
	}
	f.Seek(0, 0)
	if b, err := readSecret(f, "session file", p, 100); err != nil || len(b) != 100 {
		t.Errorf("a file exactly at the cap: %d %v", len(b), err)
	}
}

// The mode refusal tells each file its own remedy.
func TestSecretFileModeHints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for what, hint := range map[string]string{"session file": "log in again", "password file": "chmod 600"} {
		f, err := openSecret(p, what, 100)
		if f != nil {
			f.Close()
		}
		if err == nil || !strings.Contains(err.Error(), hint) || !strings.Contains(err.Error(), "0644") {
			t.Errorf("%s: %v; want a refusal that says %q", what, err, hint)
		}
	}
}

// An operating-system error about the password file keeps its prefix.
func TestPasswordFileMissingNamesTheFile(t *testing.T) {
	_, _, err := (filePassword{"u", filepath.Join(t.TempDir(), "absent")}).Get(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "password file: ") || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing password file: %v", err)
	}
}

func TestOpenSecretClosesWhenTheStatFails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := statOpen
	defer func() { statOpen = old }()
	var seen *os.File
	boom := errors.New("boom")
	statOpen = func(f *os.File) (os.FileInfo, error) { seen = f; return nil, boom }
	f, err := openSecret(p, "session file", 100)
	if f != nil || !errors.Is(err, boom) {
		t.Fatalf("got %v, %v", f, err)
	}
	if _, err := seen.Stat(); err == nil {
		t.Error("the descriptor was left open")
	}
}

func TestReadSecretReportsAReadError(t *testing.T) {
	dir, err := os.Open(t.TempDir()) // reading a directory fails
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if b, err := readSecret(dir, "session file", "d", 10); err == nil {
		t.Errorf("a read error was taken for %q", b)
	}
}

// Whatever fails after the temporary file exists leaves nothing behind and the
// destination as it was.
func TestWriteSecretFailuresLeaveNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	if err := os.WriteFile(p, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := fillSecret
	defer func() { fillSecret = old }()
	boom := errors.New("disk full")
	fillSecret = func(f *os.File, b []byte) error { f.Close(); return boom }
	if err := writeSecret(p, []byte("NEW")); !errors.Is(err, boom) {
		t.Errorf("the write failure was lost: %v", err)
	}
	fillSecret = old
	// The rename fails when the destination is a non-empty directory.
	d := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(d, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSecret(d, []byte("NEW")); err == nil {
		t.Error("a rename onto a directory succeeded")
	}
	// The temporary file cannot be created in a directory that is not there.
	var pe *os.PathError
	if err := writeSecret(filepath.Join(dir, "none", "s.json"), []byte("x")); !errors.As(err, &pe) || pe.Op != "open" {
		t.Errorf("a missing directory: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "OLD" {
		t.Errorf("the destination changed: %q", b)
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 2 { // s.json and d
		t.Errorf("%d entries, want 2 (no temporary file left)", len(es))
	}
}
