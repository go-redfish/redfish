package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const moduleRoot = "../.."

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTargetsSelectsOnlyFuzzFunctions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a_test.go", `package a
import "testing"
func FuzzA(f *testing.F) {}
func FuzzNotF(t *testing.T) {}
func FuzzTwo(f *testing.F, x int) {}
func helper(f *testing.F) {}
type T struct{}
func (T) FuzzMethod(f *testing.F) {}
`)
	write(t, dir, "sub/dir/b_test.go", "package b\nimport \"testing\"\nfunc FuzzB(f *testing.F) {}\n")
	write(t, dir, "testdata/c_test.go", "package c\nimport \"testing\"\nfunc FuzzC(f *testing.F) {}\n")
	write(t, dir, "notest.go", "package a\nimport \"testing\"\nfunc FuzzD(f *testing.F) {}\n")
	got, err := Targets(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{"FuzzA", "."}, {"FuzzB", "./sub/dir"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRunRefusesAnEmptyMatrix(t *testing.T) {
	if err := run(t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("empty module must be refused")
	}
}

func TestRunEmitsMatrixJSON(t *testing.T) {
	var b bytes.Buffer
	if err := run(moduleRoot, &b); err != nil {
		t.Fatal(err)
	}
	var m struct{ Include []Target }
	if err := json.Unmarshal(b.Bytes(), &m); err != nil || len(m.Include) == 0 {
		t.Fatalf("bad matrix %q: %v", b.String(), err)
	}
}

// ci.yml keeps a hand-written 30 s smoke matrix. This test is the gate that
// stops a new Fuzz function being forgotten there (five v0.15.1 targets went
// unscheduled until v0.16.0), and a removed one lingering.
func TestCIWorkflowListsEveryFuzzTarget(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(moduleRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := Targets(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	have := map[Target]bool{}
	re := regexp.MustCompile(`\{target: (Fuzz\w+), pkg: ([^}\s]+)\}`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		have[Target{m[1], m[2]}] = true
	}
	for _, tg := range ts {
		if !have[tg] {
			t.Errorf("%s (%s) is missing from the fuzz matrix of .github/workflows/ci.yml", tg.Target, tg.Pkg)
		}
		delete(have, tg)
	}
	for tg := range have {
		t.Errorf("ci.yml schedules %s (%s), which no longer exists", tg.Target, tg.Pkg)
	}
	if !strings.Contains(string(src), "fuzz smoke") {
		t.Error("ci.yml has no fuzz smoke job")
	}
}
