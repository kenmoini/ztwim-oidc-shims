/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package params

import (
	"strings"
	"text/template"
	"text/template/parse"
)

// parseTemplate parses tmpl the same way Render executes it: no FuncMap, and a
// reference to a key that is not in the values is an error rather than "<no value>".
func parseTemplate(name, tmpl string) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Parse(tmpl)
}

// Render executes tmpl (a Go text/template with missingkey=error and no functions) over values.
// name is used in error messages.
func Render(name, tmpl string, values Values) (string, error) {
	t, err := parseTemplate(name, tmpl)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, values); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ReferencedKeys returns the top-level field names a template references
// (e.g. "{{ .foo }}" -> "foo"), in order of first appearance and without duplicates.
func ReferencedKeys(tmpl string) ([]string, error) {
	t, err := parseTemplate("template", tmpl)
	if err != nil {
		return nil, err
	}
	if t.Tree == nil {
		return nil, nil
	}
	var keys []string
	seen := make(map[string]struct{})
	walkNode(t.Root, func(field string) {
		if _, ok := seen[field]; ok {
			return
		}
		seen[field] = struct{}{}
		keys = append(keys, field)
	})
	return keys, nil
}

// walkNode visits every node below n and reports the first identifier of each field reference.
func walkNode(n parse.Node, visit func(string)) {
	switch node := n.(type) {
	case nil:
		return
	case *parse.ListNode:
		if node == nil {
			return
		}
		for _, child := range node.Nodes {
			walkNode(child, visit)
		}
	case *parse.ActionNode:
		walkNode(node.Pipe, visit)
	case *parse.PipeNode:
		if node == nil {
			return
		}
		for _, cmd := range node.Cmds {
			walkNode(cmd, visit)
		}
	case *parse.CommandNode:
		for _, arg := range node.Args {
			walkNode(arg, visit)
		}
	case *parse.FieldNode:
		if len(node.Ident) > 0 {
			visit(node.Ident[0])
		}
	case *parse.ChainNode:
		walkNode(node.Node, visit)
	case *parse.IfNode:
		walkBranch(&node.BranchNode, visit)
	case *parse.RangeNode:
		walkBranch(&node.BranchNode, visit)
	case *parse.WithNode:
		walkBranch(&node.BranchNode, visit)
	case *parse.TemplateNode:
		walkNode(node.Pipe, visit)
	}
}

func walkBranch(b *parse.BranchNode, visit func(string)) {
	walkNode(b.Pipe, visit)
	walkNode(b.List, visit)
	walkNode(b.ElseList, visit)
}
