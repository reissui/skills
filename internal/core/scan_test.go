package core

import (
	"go/ast"
	"strings"
)

// sourceIdentsAndStrings returns a space-joined string of every identifier name
// and string-literal value in f, excluding comments. Used by the
// provider-agnostic guard test so that provider names appearing only in doc
// comments (as legitimate examples) do not trip the check.
func sourceIdentsAndStrings(f *ast.File) string {
	var sb strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			sb.WriteString(v.Name)
			sb.WriteByte(' ')
		case *ast.BasicLit:
			sb.WriteString(v.Value)
			sb.WriteByte(' ')
		}
		return true
	})
	return sb.String()
}
