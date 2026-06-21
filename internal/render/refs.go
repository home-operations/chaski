package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"
)

// references walks a parsed template tree and returns every template name it
// references, both via the {{ template "x" }} action and via the include "x"
// function call. An include whose name argument is not a string literal is an
// error: a non-literal name (a field path, a variable, a pipeline) cannot be
// resolved at load, so it can be neither checked for existence nor for a cycle —
// and could be payload-derived. We require it to be constant, which also matches
// the {{ template }} action, whose name is always a literal. The returned names
// may contain duplicates; callers that care dedupe.
func references(tree *parse.Tree) ([]string, error) {
	if tree == nil || tree.Root == nil {
		return nil, nil
	}

	var (
		refs   []string
		badErr error
	)

	var walkNode func(parse.Node)
	var walkPipe func(*parse.PipeNode)

	walkPipe = func(p *parse.PipeNode) {
		if p == nil {
			return
		}
		for _, cmd := range p.Cmds {
			if len(cmd.Args) > 0 {
				if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok && id.Ident == includeName {
					switch {
					case len(cmd.Args) < 2:
						if badErr == nil {
							badErr = fmt.Errorf(`include needs a template name, e.g. {{ include "name" . }}`)
						}
					default:
						if s, ok := cmd.Args[1].(*parse.StringNode); ok {
							refs = append(refs, s.Text)
						} else if badErr == nil {
							badErr = fmt.Errorf(`include template name must be a string literal, e.g. {{ include "name" . }}`)
						}
					}
				}
			}
			// An argument may itself be a parenthesized pipeline, e.g.
			// {{ printf "%s" (include "x" .) }}.
			for _, arg := range cmd.Args {
				if pn, ok := arg.(*parse.PipeNode); ok {
					walkPipe(pn)
				}
			}
		}
	}

	walkNode = func(n parse.Node) {
		switch x := n.(type) {
		case *parse.ListNode:
			if x == nil {
				return
			}
			for _, c := range x.Nodes {
				walkNode(c)
			}
		case *parse.ActionNode:
			walkPipe(x.Pipe)
		case *parse.IfNode:
			walkPipe(x.Pipe)
			walkNode(x.List)
			walkNode(x.ElseList)
		case *parse.RangeNode:
			walkPipe(x.Pipe)
			walkNode(x.List)
			walkNode(x.ElseList)
		case *parse.WithNode:
			walkPipe(x.Pipe)
			walkNode(x.List)
			walkNode(x.ElseList)
		case *parse.TemplateNode:
			refs = append(refs, x.Name)
			walkPipe(x.Pipe)
		}
	}

	walkNode(tree.Root)
	if badErr != nil {
		return nil, badErr
	}
	return refs, nil
}

// checkRefs verifies every name an owner references resolves to a declared
// snippet and is not a reserved name. owner is used only for attribution.
func checkRefs(owner string, refs []string, defined map[string]bool) error {
	for _, r := range refs {
		switch {
		case r == rootName || r == fieldName:
			return fmt.Errorf("render: template %q references the reserved name %q", owner, r)
		case !defined[r]:
			return fmt.Errorf("render: template %q references undefined snippet %q", owner, r)
		}
	}
	return nil
}

// checkAcyclic reports the first cycle in the snippet reference graph, naming the
// path. An include cycle is the dangerous case: include re-enters via
// ExecuteTemplate, which resets text/template's depth guard, so a cycle recurses
// until the goroutine stack overflows — a runtime-fatal crash that recover()
// cannot catch. Rejecting it here turns that into a load-time error.
func checkAcyclic(refs map[string][]string) error {
	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(refs))
	var path []string

	var visit func(string) error
	visit = func(n string) error {
		color[n] = gray
		path = append(path, n)
		for _, m := range refs[n] {
			switch color[m] {
			case gray:
				cycle := append(slices.Clone(path[slices.Index(path, m):]), m)
				return fmt.Errorf("render: template cycle: %s", strings.Join(cycle, " → "))
			case white:
				if err := visit(m); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return nil
	}

	for _, n := range slices.Sorted(maps.Keys(refs)) {
		if color[n] == white {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// extraTemplate returns the name of any template associated with t that is not in
// allowed, or "" if there is none. It catches {{ define }}/{{ block }} inside a
// snippet or field body, which would add templates the reference analysis does
// not model (and could shadow a snippet or the reserved names).
func extraTemplate(t *template.Template, allowed map[string]bool) string {
	for _, a := range t.Templates() {
		if n := a.Name(); n != "" && !allowed[n] {
			return n
		}
	}
	return ""
}
