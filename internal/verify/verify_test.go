package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/home-operations/chaski/internal/config"
)

func hmacHex(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func hmacB64(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func mustCompile(t *testing.T, v *config.Verify) *Verifier {
	t.Helper()
	vf, err := Compile(v)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return vf
}

func hdr(k, v string) http.Header {
	h := http.Header{}
	h.Set(k, v)
	return h
}

func TestGitHubPreset(t *testing.T) {
	body := `{"action":"opened"}`
	vf := mustCompile(t, &config.Verify{Provider: "github", Secret: config.StringList{"s3cr3t"}})

	good := hdr("X-Hub-Signature-256", "sha256="+hmacHex("s3cr3t", body))
	if !vf.Verify(good, []byte(body)) {
		t.Error("valid GitHub signature should verify")
	}
	if vf.Verify(good, []byte(body+"tampered")) {
		t.Error("tampered body must fail")
	}
	if vf.Verify(hdr("X-Hub-Signature-256", "sha256="+hmacHex("wrong", body)), []byte(body)) {
		t.Error("wrong secret must fail")
	}
	if vf.Verify(http.Header{}, []byte(body)) {
		t.Error("missing header must fail")
	}
}

func TestGenericHMACBase64(t *testing.T) {
	body := "payload"
	vf := mustCompile(t, &config.Verify{
		Type: "hmac", Header: "X-Sig", Algo: "sha256", Encoding: "base64",
		Secret: config.StringList{"key"},
	})
	if !vf.Verify(hdr("X-Sig", hmacB64("key", body)), []byte(body)) {
		t.Error("valid base64 HMAC should verify")
	}
	if vf.Verify(hdr("X-Sig", "not-base64!!"), []byte(body)) {
		t.Error("malformed header must fail, not panic")
	}
}

func TestToken(t *testing.T) {
	vf := mustCompile(t, &config.Verify{Type: "token", Header: "X-Gitlab-Token", Secret: config.StringList{"tok"}})
	if !vf.Verify(hdr("X-Gitlab-Token", "tok"), []byte("anything")) {
		t.Error("matching token should verify")
	}
	if vf.Verify(hdr("X-Gitlab-Token", "nope"), []byte("anything")) {
		t.Error("mismatched token must fail")
	}
}

func TestSecretRotation(t *testing.T) {
	body := "b"
	vf := mustCompile(t, &config.Verify{Provider: "github", Secret: config.StringList{"old", "new"}})
	// Signed with the second (rotated-in) secret.
	if !vf.Verify(hdr("X-Hub-Signature-256", "sha256="+hmacHex("new", body)), []byte(body)) {
		t.Error("a signature from any listed secret should verify")
	}
	if vf.Verify(hdr("X-Hub-Signature-256", "sha256="+hmacHex("retired", body)), []byte(body)) {
		t.Error("a signature from an unlisted secret must fail")
	}
}

func TestNilVerifyAcceptsAll(t *testing.T) {
	vf, err := Compile(nil)
	if err != nil || vf != nil {
		t.Fatalf("Compile(nil) = %v, %v; want nil, nil", vf, err)
	}
	if !vf.Verify(http.Header{}, nil) {
		t.Error("a nil Verifier should accept (no verify configured)")
	}
}

func TestCompileErrors(t *testing.T) {
	tests := map[string]*config.Verify{
		"unknown provider": {Provider: "bitbucket", Secret: config.StringList{"s"}},
		"unknown type":     {Type: "magic", Header: "X", Secret: config.StringList{"s"}},
		"no secret":        {Type: "token", Header: "X"},
		"hmac no header":   {Type: "hmac", Secret: config.StringList{"s"}},
		"token no header":  {Type: "token", Secret: config.StringList{"s"}},
		"bad encoding":     {Type: "hmac", Header: "X", Encoding: "rot13", Secret: config.StringList{"s"}},
		"bad algo":         {Type: "hmac", Header: "X", Algo: "md5", Secret: config.StringList{"s"}},
	}
	for name, v := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(v); err == nil {
				t.Errorf("Compile(%s) = nil error, want error", name)
			}
		})
	}
}
