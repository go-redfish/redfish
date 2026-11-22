// Command fuzzmatrix prints the GitHub Actions matrix of every fuzz target in
// the module, found by parsing the *_test.go files, so a new Fuzz function is
// scheduled by fuzz-long.yml without anyone listing it by hand.
//
// Usage: go run ./internal/fuzzmatrix [root]   (root defaults to ".")
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Target is one fuzz function and the package directory that holds it.
type Target struct {
	Target string `json:"target"`
	Pkg    string `json:"pkg"`
}

// Targets returns every `func FuzzX(f *testing.F)` under root, sorted by
// package then name. Pkg is "." or "./sub/dir", as `go test` expects it.
func Targets(root string) ([]Target, error) {
	var out []Target
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			n := d.Name()
			if p != root && (n == "testdata" || n == ".git" || strings.HasPrefix(n, "_")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		pkg := "."
		if rel != "." {
			pkg = "./" + path.Clean(filepath.ToSlash(rel))
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") || !isTestingF(fn) {
				continue
			}
			out = append(out, Target{Target: fn.Name.Name, Pkg: pkg})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pkg != out[j].Pkg {
			return out[i].Pkg < out[j].Pkg
		}
		return out[i].Target < out[j].Target
	})
	return out, nil
}

func isTestingF(fn *ast.FuncDecl) bool {
	ps := fn.Type.Params.List
	if len(ps) != 1 {
		return false
	}
	star, ok := ps[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "F" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "testing"
}

// run writes {"include":[...]} for root to w. An empty list is an error: a
// matrix with no job would let the workflow pass having fuzzed nothing.
func run(root string, w io.Writer) error {
	ts, err := Targets(root)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		return fmt.Errorf("no fuzz target found under %s", root)
	}
	return json.NewEncoder(w).Encode(map[string][]Target{"include": ts})
}

var osExit = os.Exit

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := run(root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fuzzmatrix:", err)
		osExit(1)
	}
}
