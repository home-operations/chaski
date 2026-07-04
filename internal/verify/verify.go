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
// malformed block (no variant or more than one, missing header, unsupported
// algo/encoding, no secret). A nil block yields a nil Verifier.
func Compile(v *config.Verify) (*Verifier, error) {
	if v == nil {
		return nil, nil
	}
	switch {
	case v.GitHub != nil && v.HMAC == nil && v.Token == nil:
		// X-Hub-Signature-256: sha256=<hex(hmac-sha256(body))>.
		return compileHMAC("X-Hub-Signature-256", algoSHA256, encHex, "sha256=", v.GitHub.Secret)
	case v.HMAC != nil && v.GitHub == nil && v.Token == nil:
		return compileHMAC(v.HMAC.Header, v.HMAC.Algo, v.HMAC.Encoding, v.HMAC.Prefix, v.HMAC.Secret)
	case v.Token != nil && v.GitHub == nil && v.HMAC == nil:
		return compileToken(v.Token.Header, v.Token.Secret)
	default:
		return nil, errors.New("verify: set exactly one of github, hmac, or token")
	}
}

// compileHMAC builds an HMAC verifier; encoding defaults to hex, algo to sha256.
func compileHMAC(header, algo, encoding, prefix string, secret config.StringList) (*Verifier, error) {
	if header == "" {
		return nil, errors.New("verify: hmac requires a header")
	}
	secrets, err := secretBytes(secret)
	if err != nil {
		return nil, err
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
	return &Verifier{mode: modeHMAC, header: header, prefix: prefix, encoding: encoding, newHash: nh, secrets: secrets}, nil
}

// compileToken builds a shared-token verifier.
func compileToken(header string, secret config.StringList) (*Verifier, error) {
	if header == "" {
		return nil, errors.New("verify: token requires a header")
	}
	secrets, err := secretBytes(secret)
	if err != nil {
		return nil, err
	}
	return &Verifier{mode: modeToken, header: header, secrets: secrets}, nil
}

// secretBytes captures the configured secrets, requiring at least one and
// rejecting any empty/whitespace-only value. An empty secret is a fail-open: it
// builds an empty HMAC key (forgeable by anyone) or an empty token, and the
// strict env funcmap only catches an *unset* var, not one set to "". Secrets are
// stored verbatim (no trimming) — only the emptiness check trims.
func secretBytes(secret config.StringList) ([][]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("verify: at least one secret is required")
	}
	secrets := make([][]byte, len(secret))
	for i, s := range secret {
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("verify: a secret must not be empty")
		}
		secrets[i] = []byte(s)
	}
	return secrets, nil
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
		// A configured prefix is part of the signature contract (GitHub's
		// "sha256=..."): a header without it is malformed, not close enough.
		sig, ok := strings.CutPrefix(got, vf.prefix)
		if !ok {
			return false
		}
		provided, err := decodeMAC(vf.encoding, sig)
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
