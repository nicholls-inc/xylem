package invariants_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvariantTestBodiesNotEmpty enforces that every Test* function in every
// *_invariants_prop_test.go file has at least one executable statement.
// Comment-only or blank bodies are not acceptable — use t.Skip("reason") for
// aspirational or known-violation tests.
func TestInvariantTestBodiesNotEmpty(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "*_invariants_prop_test.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no *_invariants_prop_test.go files found — check that the package is in cli/internal/")
	}

	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", file, err)
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if len(fn.Body.List) == 0 {
				pos := fset.Position(fn.Pos())
				t.Errorf("%s: %s has an empty body — add t.Skip(\"reason\") or implement the test",
					pos, fn.Name.Name)
			}
		}
	}
}
