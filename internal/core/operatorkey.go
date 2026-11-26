package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// operatorKeyBytes is the random length of the key, as secrets.token_urlsafe(32).
const operatorKeyBytes = 32

// operatorKey holds the SHA-256 digest of the current key, never the key itself.
type operatorKey struct {
	digest [sha256.Size]byte
}

// matches compares a candidate with the key in constant time. Both sides are hashed first,
// so the comparison always covers 32 bytes and its time does not depend on the
// candidate's length or on where it differs.
func (key operatorKey) matches(candidate string) bool {
	digest := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(digest[:], key.digest[:]) == 1
}

// newOperatorKeyText returns 32 random bytes in unpadded URL-safe base64, the format of
// secrets.token_urlsafe(32): 43 characters.
func newOperatorKeyText() (string, error) {
	raw := make([]byte, operatorKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// writeOperatorKey replaces operator.key at each start, so a key from an earlier core
// stops working. Port of write_operator_key in core.py: the key goes to a temporary file
// opened with O_EXCL and mode 0600, which refuses an existing file or symbolic link, and
// is then renamed over operator.key.
func writeOperatorKey(dataDir string) (operatorKey, error) {
	text, err := newOperatorKeyText()
	if err != nil {
		return operatorKey{}, err
	}
	temporary := filepath.Join(dataDir, operatorKeyFileName+".tmp")
	if err := os.Remove(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return operatorKey{}, err
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return operatorKey{}, err
	}
	_, writeErr := file.WriteString(text)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return operatorKey{}, err
	}
	if err := os.Rename(temporary, filepath.Join(dataDir, operatorKeyFileName)); err != nil {
		return operatorKey{}, err
	}
	return operatorKey{digest: sha256.Sum256([]byte(text))}, nil
}
