package supplierrepo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Cursors are authenticated, opaque and bound to the normalized search filters.
// A repository instance owns its random key; after a service restart clients
// start a fresh traversal. No deployment secret or stored pagination state is
// needed. The immutable version reference keeps tokens small even for long
// Unicode names and preserves the anchor's ordering key after rename/deletion.
func newCursorCipher() cipher.AEAD {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func cursorFilters(filter supplier.ListFilter) []byte {
	return []byte(supplier.NormalizeName(filter.Query) + "\x00" + strings.ToLower(strings.TrimSpace(filter.Type)))
}

func (r *Repository) encodeCursor(versionID supplier.VersionID, filter supplier.ListFilter) string {
	token := r.cursorCipher.Seal(nil, nil, []byte(versionID), cursorFilters(filter))
	return base64.RawURLEncoding.EncodeToString(token)
}

func (r *Repository) decodeCursor(value string, filter supplier.ListFilter) (supplier.VersionID, error) {
	token, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(value) > 512 || base64.RawURLEncoding.EncodeToString(token) != value {
		return "", invalidArgument("cursor is invalid")
	}
	decoded, err := r.cursorCipher.Open(nil, nil, token, cursorFilters(filter))
	if err != nil || !supplier.ValidVersionID(supplier.VersionID(decoded)) {
		return "", invalidArgument("cursor is invalid")
	}
	return supplier.VersionID(decoded), nil
}
