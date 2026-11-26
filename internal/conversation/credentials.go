package conversation

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"regexp"
)

// inviteLifetimeSeconds is how long a single-use join code stays valid.
const inviteLifetimeSeconds = 3600

// joinCodePattern matches a workspace-tagged code: a 22-character nonce, a dot, and a
// 12-digit hex tag (JOIN_CODE_PATTERN in repository.py, used with fullmatch).
var joinCodePattern = regexp.MustCompile(`^([A-Za-z0-9_-]{22})\.([0-9a-f]{12})$`)

// digest returns the SHA-256 hex digest that the store keeps instead of a bearer
// credential.
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// joinWorkspaceTag derives the per-code workspace tag without embedding a path.
func joinWorkspaceTag(workspace, nonce string) string {
	return digest(workspace + ":" + nonce)[:12]
}

// randomBytes returns count bytes from the operating system's random source.
func randomBytes(count int) []byte {
	buffer := make([]byte, count)
	// crypto/rand.Read never fails on the supported systems and panics rather than
	// return short output.
	_, _ = rand.Read(buffer)
	return buffer
}

// tokenURLSafe is secrets.token_urlsafe(count): count random bytes in unpadded URL-safe
// base64.
func tokenURLSafe(count int) string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(count))
}

// tokenHex is secrets.token_hex(count): count random bytes in hex.
func tokenHex(count int) string {
	return hex.EncodeToString(randomBytes(count))
}

// newParticipantToken returns a new participant credential.
func newParticipantToken() string {
	return tokenURLSafe(32)
}

// newOpenInviteCode returns a workspace-tagged reusable invite code and its digest.
func newOpenInviteCode(workspace string) (string, string) {
	nonce := tokenURLSafe(16)
	code := nonce + "." + joinWorkspaceTag(workspace, nonce)
	return code, digest(code)
}

// joinCode is a new single-use code with its stored digest and expiry.
type joinCode struct {
	code    string
	hash    string
	expires int64
}

// newJoinCode returns a workspace-tagged single-use code that expires after
// inviteLifetimeSeconds.
func newJoinCode(workspace string, now int64) joinCode {
	code, hash := newOpenInviteCode(workspace)
	return joinCode{code: code, hash: hash, expires: now + inviteLifetimeSeconds}
}

// codeTagMatches reports whether a code is untagged (false, false) or tagged, and then
// whether its tag belongs to workspace.
func codeTagMatches(code, workspace string) (tagged bool, matches bool) {
	parts := joinCodePattern.FindStringSubmatch(code)
	if parts == nil {
		return false, false
	}
	expected := joinWorkspaceTag(workspace, parts[1])
	return true, subtle.ConstantTimeCompare([]byte(parts[2]), []byte(expected)) == 1
}
