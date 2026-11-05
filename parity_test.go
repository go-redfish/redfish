package redfish_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PARITY.md is a promise: every row is done, N/A or todo with a release, and a
// done row names tests that exist. When the work is complete (allowTodo false),
// a todo row fails this test.
const allowTodo = true

func TestParityTableIsHonest(t *testing.T) {
	doc, err := os.ReadFile("PARITY.md")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{}
	err = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range regexp.MustCompile(`func ((?:Test|Fuzz)\w+)\(`).FindAllStringSubmatch(string(b), -1) {
			tests[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, done, todo := 0, 0, 0
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "| redfish_") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), " | ")
		if len(cells) != 7 {
			t.Errorf("a row without 7 cells: %q", line)
			continue
		}
		rows++
		status := strings.TrimSpace(cells[6])
		switch {
		case status == "done":
			done++
			names := regexp.MustCompile("`(Test\\w+|Fuzz\\w+)`").FindAllStringSubmatch(cells[5], -1)
			if len(names) == 0 || cells[3] == "" || cells[4] == "" {
				t.Errorf("a done row without function, CLI or test: %q", line)
			}
			for _, n := range names {
				if !tests[n[1]] {
					t.Errorf("row %s %s names the test %s, which does not exist", cells[0], cells[2], n[1])
				}
			}
		case strings.HasPrefix(status, "N/A"):
		case strings.HasPrefix(status, "todo ("):
			todo++
			if !allowTodo {
				t.Errorf("a todo row: %s %s %s", cells[0], cells[1], cells[2])
			}
		default:
			t.Errorf("unknown status %q in %q", status, line)
		}
	}
	// The oracle's table, counted: 46 redfish_command, 14 redfish_config and
	// 39 redfish_info commands (community.general 33e521d6).
	if rows != 99 {
		t.Errorf("%d rows, want 99 (46 + 14 + 39)", rows)
	}
	t.Logf("%d rows: %d done, %d todo", rows, done, todo)
}
