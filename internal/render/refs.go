package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template/parse"
)

// references walks a parsed template tree and returns every template name it
// references, both via the {{ template "x" }} action and via the include "x"
// function call. It is the soundness-critical half of the load-time cycle check:
// if a reference could hide in a node the walk skips, a cycle would slip through
// to a render-time stack overflow. So the walk is fail-closed — it descends into
// every node that can carry a pipeline (including a parenthesized pipeline behind
// a field access, a *parse.ChainNode) and returns an error on any node type it
// does not recognize, rather than treating an unknown node as reference-free.
//
// An include whose name argument is not a string literal is an error: a
// non-literal name (a field path, a variable, a pipeline, or one supplied by
// pipe) cannot be resolved at load, so it can be neither checked for existence
// nor for a cycle — and could be payload-derived. Requiring a literal matches the
// {{ template }} action, whose name is always literal. Returned names may contain
// duplicates; callers that care dedupe.
func references(tree *parse.Tree) ([]string, error) {
	if tree == nil || tree.Root == nil {
		return nil, nil
	}
	w := &refWalker{}
	w.node(tree.Root)
	if w.err != nil {
		return nil, w.err
	}
	return w.refs, nil
}

// refWalker accumulates references and the first walk error as it descends a
// parse tree.
type refWalker struct {
	refs []string
	err  error
}

func (w *refWalker) badInclude() {
	if w.err == nil {
		w.err = fmt.Errorf(`include must be called directly with a string-literal name, e.g. {{ include "name" . }}`)
	}
}

func (w *refWalker) node(n parse.Node) {
	if n == nil || w.err != nil {
		return
	}
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			w.node(c)
		}
	case *parse.ActionNode:
		w.pipe(x.Pipe)
	case *parse.IfNode:
		w.branch(x.Pipe, x.List, x.ElseList)
	case *parse.RangeNode:
		w.branch(x.Pipe, x.List, x.ElseList)
	case *parse.WithNode:
		w.branch(x.Pipe, x.List, x.ElseList)
	case *parse.TemplateNode:
		w.refs = append(w.refs, x.Name)
		w.pipe(x.Pipe)
	case *parse.PipeNode:
		w.pipe(x)
	case *parse.ChainNode:
		w.node(x.Node)
	case *parse.TextNode, *parse.BoolNode, *parse.DotNode, *parse.FieldNode,
		*parse.IdentifierNode, *parse.NilNode, *parse.NumberNode,
		*parse.StringNode, *parse.VariableNode, *parse.CommentNode,
		*parse.BreakNode, *parse.ContinueNode:
		// Leaf nodes — they cannot hold a template/include reference.
	default:
		// Fail closed: an unmodeled node could hide a reference, and treating it
		// as reference-free is how a cycle would reach a render crash.
		if w.err == nil {
			w.err = fmt.Errorf("unsupported template node %T", n)
		}
	}
}

// branch walks the pipe, body, and else of an if/range/with.
func (w *refWalker) branch(pipe *parse.PipeNode, list, elseList *parse.ListNode) {
	w.pipe(pipe)
	w.node(list)
	w.node(elseList)
}

func (w *refWalker) pipe(p *parse.PipeNode) {
	if p == nil || w.err != nil {
		return
	}
	for _, cmd := range p.Cmds {
		w.command(cmd)
	}
}

func (w *refWalker) command(cmd *parse.CommandNode) {
	args := cmd.Args
	// include must appear ONLY as a command head with a string-literal name.
	// Anywhere else — not the head (so passed to call, printf, …) or the head
	// without a literal name (bound to a variable, name supplied by pipe or
	// parens) — means include is invoked indirectly, with a name that can't be
	// resolved at load, so a cycle through it could slip past the graph check to
	// a render-time stack overflow. Scan every position.
	for i, arg := range args {
		id, ok := arg.(*parse.IdentifierNode)
		if !ok || id.Ident != includeName {
			continue
		}
		if i == 0 && len(args) >= 2 {
			if sn, ok := args[1].(*parse.StringNode); ok {
				w.refs = append(w.refs, sn.Text)
				continue
			}
		}
		w.badInclude()
	}
	// An argument may itself carry a pipeline: a bare parenthesized pipeline
	// (*parse.PipeNode) or one followed by a field/method access (*parse.ChainNode,
	// e.g. (include "x" .).Field). Both must be walked or a reference inside them
	// is missed.
	for _, arg := range args {
		switch a := arg.(type) {
		case *parse.PipeNode:
			w.pipe(a)
		case *parse.ChainNode:
			w.node(a)
		}
	}
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
