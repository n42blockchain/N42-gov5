package evmsdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// gomobile binds this package for the phones (make android, make ios), and
// gobind refuses the whole package, not just the one function, when an
// exported package function returns more than one value besides an error.
// (A method it cannot bind it skips.) gobind is not a dependency of the
// repository, so the rule is checked on the source.
func TestExportedFunctionsAreBindable(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil {
					continue
				}
				var results []ast.Expr
				for _, f := range fd.Type.Results.List {
					for range max(len(f.Names), 1) {
						results = append(results, f.Type)
					}
				}
				last, _ := results[len(results)-1].(*ast.Ident)
				if len(results) > 2 || (len(results) == 2 && (last == nil || last.Name != "error")) {
					t.Errorf("%s: %s returns %d values; gobind takes one value and optionally an error",
						fset.Position(fd.Pos()), fd.Name.Name, len(results))
				}
			}
		}
	}
}
