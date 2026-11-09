package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is a dependency guard, not a check for particular filenames: it rejects
// future accidental SQL/HTTP coupling and imports between feature slices.
func TestArchitectureDependencies(t *testing.T) {
	const module = "github.com/AskarKasimov/ai-tutor/services/backend/internal/"
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			target, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			internal := strings.TrimPrefix(target, module)
			if strings.HasPrefix(target, module) {
				switch parts[0] {
				case "shared":
					if !withinPackage(internal, "shared") {
						t.Errorf("shared must be domain-agnostic: %s imports %s", relative, target)
					}
				case "entities":
					// Diagnostic evidence reuses the common exam grading rules.
					usesExamRules := parts[1] == "diagnostic" && withinPackage(internal, "entities/exam")
					if !withinPackage(internal, "shared") && !withinPackage(internal, "entities/"+parts[1]) && !usesExamRules {
						t.Errorf("entity imports an outer layer: %s -> %s", relative, target)
					}
				case "features":
					if withinPackage(internal, "app") || (withinPackage(internal, "features") && !withinPackage(internal, "features/"+parts[1])) {
						t.Errorf("feature crosses its slice: %s -> %s", relative, target)
					}
					if parts[2] == "application" && (withinPackage(internal, "features") && !withinPackage(internal, "features/"+parts[1]+"/application")) {
						t.Errorf("application imports adapter: %s -> %s", relative, target)
					}
					if parts[2] == "application" && (strings.HasPrefix(internal, "shared/httpx") || strings.HasPrefix(internal, "shared/postgres")) {
						t.Errorf("application imports infrastructure: %s -> %s", relative, target)
					}
					if parts[2] == "transport" && strings.Contains(internal, "/infrastructure/") {
						t.Errorf("transport must receive ports through injection: %s -> %s", relative, target)
					}
					if parts[2] == "infrastructure" && strings.Contains(internal, "/transport/") {
						t.Errorf("infrastructure imports transport: %s -> %s", relative, target)
					}
				}
			}
			pure := parts[0] == "entities" || (parts[0] == "features" && parts[2] == "application")
			if pure && (withinPackage(internal, "shared/httpx") || withinPackage(internal, "shared/postgres")) {
				t.Errorf("pure layer imports infrastructure: %s -> %s", relative, target)
			}
			if pure && (target == "net/http" || strings.Contains(target, "github.com/jackc/") || strings.HasPrefix(target, "database/sql")) {
				t.Errorf("pure layer depends on transport/storage: %s -> %s", relative, target)
			}
		}
		if parts[0] == "app" {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "Exec", "Query", "QueryRow":
					t.Errorf("composition root contains database operations: %s (%s)", relative, selector.Sel.Name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func withinPackage(target, root string) bool {
	return target == root || strings.HasPrefix(target, root+"/")
}
