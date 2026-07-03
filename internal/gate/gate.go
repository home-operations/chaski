// Package gate compiles and evaluates a route's whenExpr — the single CEL
// boolean that decides whether chaski acts on a request. CEL is a
// safe, bounded, non-Turing-complete language (no I/O, no unbounded loops), so
// an operator-supplied predicate over an untrusted payload can't hang or
// escape. Each expression is compiled and type-checked once at load (fail-fast)
// and evaluated under a cost limit and the request's context deadline.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"
)

// evalCostLimit caps the CEL cost of one evaluation — defense-in-depth against a
// pathological operator expression (CEL has no loops, so a finite cost always
// exists). No reasonable gate approaches this ceiling.
const evalCostLimit = 1_000_000

// interruptCheckFrequency is how often (in CEL steps) evaluation checks the
// context, so ContextEval honours the request deadline promptly.
const interruptCheckFrequency = 1024

// structpbValueType is the JSON-bridge type CEL values convert to cleanly
// (string-keyed maps, JSON-native scalars) for toJSON.
var structpbValueType = reflect.TypeOf(&structpb.Value{})

// Input is the variable environment a whenExpr is evaluated against.
type Input struct {
	Payload any               // decoded body; dyn (map, list, or scalar)
	Headers map[string]string // lower-cased header names
	Query   map[string]string // first value per query key
	Method  string
	Route   string
	Now     time.Time
}

// Gate is a compiled whenExpr. The zero gate (from an empty expression) always
// fires, so a route without a whenExpr acts on every request (default true).
type Gate struct {
	prg    cel.Program
	src    string
	always bool
}

// Compile parses and type-checks expr. An empty expression yields an
// always-true gate. A syntactically invalid expression, an unknown
// variable/function, or one that cannot produce a boolean is an error.
func Compile(expr string) (*Gate, error) {
	if strings.TrimSpace(expr) == "" {
		return &Gate{always: true}, nil
	}

	env, err := newEnv()
	if err != nil {
		return nil, fmt.Errorf("gate: build env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("gate: %w", iss.Err())
	}
	// The gate is a yes/no decision. Field access on dyn-valued vars (payload)
	// is statically dyn, so accept bool or dyn; a wrong-typed literal (42, "x")
	// is rejected here, and Eval enforces an actual boolean at runtime.
	switch ast.OutputType().Kind() {
	case types.BoolKind, types.DynKind:
	default:
		return nil, fmt.Errorf("gate: whenExpr must evaluate to a boolean, got %s", ast.OutputType())
	}
	prg, err := env.Program(ast,
		cel.CostLimit(evalCostLimit),
		cel.InterruptCheckFrequency(interruptCheckFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("gate: program: %w", err)
	}
	return &Gate{prg: prg, src: expr}, nil
}

// Eval reports whether the gate fires for in. It honours ctx's deadline and
// returns an error on a runtime fault or a non-boolean result (the caller maps
// that to an operator-fault 500).
func (g *Gate) Eval(ctx context.Context, in Input) (bool, error) {
	if g.always {
		return true, nil
	}
	out, _, err := g.prg.ContextEval(ctx, map[string]any{
		"payload": in.Payload,
		"headers": in.Headers,
		"query":   in.Query,
		"method":  in.Method,
		"route":   in.Route,
		"now":     in.Now,
	})
	if err != nil {
		return false, fmt.Errorf("gate: eval %q: %w", g.src, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("gate: %q produced %T, want bool", g.src, out.Value())
	}
	return b, nil
}

// Source returns the original expression text (for logs and diagnostics).
func (g *Gate) Source() string { return g.src }

func newEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("payload", cel.DynType),
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("query", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("method", cel.StringType),
		cel.Variable("route", cel.StringType),
		cel.Variable("now", cel.TimestampType),
		ext.Strings(),
		// Kubernetes-style ip()/cidr()/isIP()/isCIDR() + containment checks.
		ext.Network(),
		// Version 0 = base64.encode/decode only; JSON stays on toJSON below.
		ext.Encoders(ext.EncodersVersion(0)),
		toJSONFunc(),
		truncateFunc(),
	)
}

// toJSONFunc registers toJSON(v) -> string, the canonical JSON encoding of any
// value (useful with the strings extension, e.g. toJSON(payload).contains(...)).
func toJSONFunc() cel.EnvOption {
	return cel.Function("toJSON",
		cel.Overload("toJSON_dyn_string", []*cel.Type{cel.DynType}, cel.StringType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				pb, err := v.ConvertToNative(structpbValueType)
				if err != nil {
					return types.NewErr("toJSON: %v", err)
				}
				sv, ok := pb.(*structpb.Value)
				if !ok {
					return types.NewErr("toJSON: unexpected %T", pb)
				}
				b, err := json.Marshal(sv.AsInterface())
				if err != nil {
					return types.NewErr("toJSON: %v", err)
				}
				return types.String(b)
			})))
}

// truncateFunc registers truncate(s, n) -> string, a rune-safe prefix of s.
func truncateFunc() cel.EnvOption {
	return cel.Function("truncate",
		cel.Overload("truncate_string_int_string", []*cel.Type{cel.StringType, cel.IntType}, cel.StringType,
			cel.BinaryBinding(func(sv, nv ref.Val) ref.Val {
				s, ok := sv.Value().(string)
				if !ok {
					return types.NewErr("truncate: first argument must be a string")
				}
				n, ok := nv.Value().(int64)
				if !ok {
					return types.NewErr("truncate: second argument must be an int")
				}
				if n < 0 {
					n = 0
				}
				r := []rune(s)
				if int64(len(r)) <= n {
					return types.String(s)
				}
				return types.String(string(r[:n]))
			})))
}
