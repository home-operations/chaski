// Package verify implements a route's optional inbound signature check: an
// HMAC over the raw request body, or a shared-secret token in a header. It is
// compiled once at load (preset resolved, secrets captured) and
// run per request before any decode or CEL/template work. Comparisons are
// constant-time, and verify.secret may be a list so secrets rotate without
// downtime. Secrets are never compared in CEL — that path isn't constant-time
// and a whenExpr-as-auth check fails open.
package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"

	"github.com/home-operations/chaski/internal/config"
)

type mode int

const (
	modeHMAC mode = iota
	modeToken
)

const (
	typeHMAC   = "hmac"
	typeToken  = "token"
	encHex     = "hex"
	encBase64  = "base64"
	algoSHA256 = "sha256"
)

// Verifier checks one route's inbound requests. A nil *Verifier (from a route
// with no verify block) accepts everything — the caller skips verification.
type Verifier struct {
	mode     mode
	header   string
	prefix   string
	encoding string // hmac: "hex" | "base64"
	newHash  func() hash.Hash
	secrets  [][]byte
}

// Compile resolves a config.Verify into a runnable Verifier, failing fast on a
// malformed block (unknown provider/type, missing header, unsupported
// algo/encoding, no secret). A nil block yields a nil Verifier.
func Compile(v *config.Verify) (*Verifier, error) {
	if v == nil {
		return nil, nil
	}

	typ, header, algo, encoding, prefix := v.Type, v.Header, v.Algo, v.Encoding, v.Prefix
	if v.Provider != "" {
		switch strings.ToLower(v.Provider) {
		case "github":
			// X-Hub-Signature-256: sha256=<hex(hmac-sha256(body))>.
			typ, header, algo, encoding, prefix = typeHMAC, "X-Hub-Signature-256", algoSHA256, encHex, "sha256="
		default:
			return nil, fmt.Errorf("verify: unknown provider %q", v.Provider)
		}
	}

	if len(v.Secret) == 0 {
		return nil, errors.New("verify: at least one secret is required")
	}
	secrets := make([][]byte, len(v.Secret))
	for i, s := range v.Secret {
		secrets[i] = []byte(s)
	}
	vf := &Verifier{header: header, prefix: prefix, secrets: secrets}

	switch strings.ToLower(typ) {
	case typeHMAC:
		if header == "" {
			return nil, errors.New("verify: hmac requires a header")
		}
		if encoding == "" {
			encoding = encHex
		}
		if encoding != encHex && encoding != encBase64 {
			return nil, fmt.Errorf("verify: unsupported encoding %q (want hex or base64)", encoding)
		}
		if algo == "" {
			algo = algoSHA256
		}
		nh, err := hashFor(algo)
		if err != nil {
			return nil, err
		}
		vf.mode, vf.encoding, vf.newHash = modeHMAC, encoding, nh
	case typeToken:
		if header == "" {
			return nil, errors.New("verify: token requires a header")
		}
		vf.mode = modeToken
	default:
		return nil, fmt.Errorf("verify: type must be hmac or token (got %q)", typ)
	}
	return vf, nil
}

// Verify reports whether the request's header satisfies the configured check
// against the raw body. A malformed or missing header is a quiet false (→ 401),
// never an error.
func (vf *Verifier) Verify(headers http.Header, body []byte) bool {
	if vf == nil {
		return true
	}
	got := headers.Get(vf.header)
	if got == "" {
		return false
	}

	switch vf.mode {
	case modeToken:
		gotBytes := []byte(got)
		for _, s := range vf.secrets {
			if subtle.ConstantTimeCompare(gotBytes, s) == 1 {
				return true
			}
		}
		return false
	case modeHMAC:
		provided, err := decodeMAC(vf.encoding, strings.TrimPrefix(got, vf.prefix))
		if err != nil {
			return false
		}
		for _, s := range vf.secrets {
			m := hmac.New(vf.newHash, s)
			m.Write(body)
			if hmac.Equal(m.Sum(nil), provided) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func hashFor(algo string) (func() hash.Hash, error) {
	switch strings.ToLower(algo) {
	case algoSHA256:
		return sha256.New, nil
	case "sha512":
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("verify: unsupported algo %q (want sha256 or sha512)", algo)
	}
}

func decodeMAC(encoding, s string) ([]byte, error) {
	if encoding == encBase64 {
		return base64.StdEncoding.DecodeString(s)
	}
	return hex.DecodeString(s)
}
