package create

import (
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined over SHA-1; this is naming, not security.
	"encoding/hex"
)

// errandNamespace is the UUIDv5 namespace of errand IDs. It is arbitrary but
// must never change: a replay finds its errand only if it derives the same ID.
var errandNamespace = [16]byte{
	0x6f, 0x1c, 0x2b, 0x8e, 0x4d, 0x3a, 0x4c, 0x59,
	0x9e, 0x0b, 0x2a, 0x7d, 0x5f, 0x8c, 0x1e, 0x43,
}

// errandID derives the errand ID from the requester and their
// Idempotency-Key as a name-based UUID (RFC 9562, version 5). The Store port
// has no lookup by idempotency key, so this is how a replay finds the
// original errand with Store.Get before the body is read: the contract says
// a replay returns it whatever the replay's body contains (NFR7.3).
//
// key is a lower-case UUID, so it has a fixed length and the NUL separator
// keeps (requester, key) pairs from colliding.
func errandID(requesterID, key string) string {
	hash := sha1.New() //nolint:gosec // see the import.
	hash.Write(errandNamespace[:])
	hash.Write([]byte(requesterID))
	hash.Write([]byte{0})
	hash.Write([]byte(key))

	var u [16]byte
	copy(u[:], hash.Sum(nil))
	u[6] = u[6]&0x0f | 0x50 // version 5
	u[8] = u[8]&0x3f | 0x80 // RFC 9562 variant

	var out [36]byte
	hex.Encode(out[0:8], u[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], u[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], u[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], u[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], u[10:16])
	return string(out[:])
}
