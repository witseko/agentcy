package shim

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// requestStateTTL bounds how long a client may take to answer an input request, as the
// Python SDK's default policy does.
const requestStateTTL = 600 * time.Second

// errInvalidRequestState covers every way a requestState can fail verification.
var errInvalidRequestState = errors.New("invalid request state")

// requestStateSigner seals the requestState of an input_required result and verifies
// the copy a client echoes back. A state is bound to the tool name and the digest of the
// call's arguments and expires after requestStateTTL, so a client cannot answer a
// question it was never asked, or reuse an answer for another call. The key is random
// per process, so a restarted shim rejects old states.
type requestStateSigner struct {
	key []byte
	now func() time.Time
}

// newRequestStateSigner returns a signer with a fresh random key.
func newRequestStateSigner(now func() time.Time) (*requestStateSigner, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &requestStateSigner{key: key, now: now}, nil
}

// seal returns the opaque token for state, bound to one tool call.
func (signer *requestStateSigner) seal(tool string, arguments wire.Value, state string) string {
	claims := wire.NewObject()
	claims.Set("v", wire.IntValue(1))
	claims.Set("exp", wire.IntValue(signer.now().Add(requestStateTTL).Unix()))
	claims.Set("t", wire.TextValue(tool))
	claims.Set("a", wire.TextValue(argumentsDigest(arguments)))
	claims.Set("s", wire.TextValue(state))
	payload := base64.RawURLEncoding.EncodeToString([]byte(claims.Compact()))
	return payload + "." + base64.RawURLEncoding.EncodeToString(signer.mac(payload))
}

// verify checks the token's signature, expiry and binding to this tool call, and returns
// the sealed state.
func (signer *requestStateSigner) verify(token, tool string, arguments wire.Value) (string, error) {
	payload, signature, found := strings.Cut(token, ".")
	if !found {
		return "", errInvalidRequestState
	}
	given, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(given, signer.mac(payload)) {
		return "", errInvalidRequestState
	}
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", errInvalidRequestState
	}
	claims, err := wire.Parse(decoded)
	if err != nil {
		return "", errInvalidRequestState
	}
	expiry, _ := claims.Get("exp")
	seconds, ok := expiry.IntOf()
	if !ok || signer.now().Unix() >= seconds {
		return "", errInvalidRequestState
	}
	if textField(claims, "t") != tool || textField(claims, "a") != argumentsDigest(arguments) {
		return "", errInvalidRequestState
	}
	return textField(claims, "s"), nil
}

// mac returns the HMAC-SHA256 of a payload under the process key.
func (signer *requestStateSigner) mac(payload string) []byte {
	digest := hmac.New(sha256.New, signer.key)
	digest.Write([]byte(payload))
	return digest.Sum(nil)
}

// argumentsDigest hashes the arguments in a canonical form, so that a client that
// re-encodes the same arguments still matches.
func argumentsDigest(arguments wire.Value) string {
	digest := sha256.Sum256([]byte(canonical(arguments).Compact()))
	return hex.EncodeToString(digest[:16])
}

// canonical returns a copy of value with the keys of every object sorted.
func canonical(value wire.Value) wire.Value {
	switch value.Kind {
	case wire.Array:
		items := make([]wire.Value, len(value.Items))
		for index, item := range value.Items {
			items[index] = canonical(item)
		}
		return wire.ListValue(items)
	case wire.Object:
		return sortedObject(value, canonical)
	default:
		return value
	}
}

// sortedObject returns an object with the keys of object in sorted order, each value
// passed through convert.
func sortedObject(object wire.Value, convert func(wire.Value) wire.Value) wire.Value {
	keys := object.Keys()
	sortTexts(keys)
	sorted := wire.NewObject()
	for _, key := range keys {
		member, _ := object.Get(key)
		sorted.Set(key, convert(member))
	}
	return sorted
}
