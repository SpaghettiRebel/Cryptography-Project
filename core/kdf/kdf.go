// Пакет kdf выводит сеансовый ключ из общего секрета.
package kdf

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"cryptochat/core/cipher"
)

// SharedSecretSize — ожидаемая длина общего секрета в байтах.
const SharedSecretSize = 256

var (
	ErrInvalidLength = errors.New("kdf: недопустимая длина результата")
	ErrInvalidSecret = errors.New("kdf: недопустимая длина общего секрета")
)

const sessionKeyLabel = "cryptochat/session-key/v1"

func hmacSHA256(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// HKDF — RFC 5869 с SHA-256.
func HKDF(ikm, salt, info []byte, length int) ([]byte, error) {
	if length <= 0 || length > 255*sha256.Size {
		return nil, ErrInvalidLength
	}
	if salt == nil {
		salt = make([]byte, sha256.Size)
	}
	prk := hmacSHA256(salt, ikm)
	out := make([]byte, 0, length+sha256.Size)
	var t []byte
	for i := byte(1); len(out) < length; i++ {
		t = hmacSHA256(prk, t, info, []byte{i})
		out = append(out, t...)
	}
	return out[:length], nil
}

// SessionKey выводит ключ длины keyBits/8. Порядок сторон не входит в контекст, поэтому обе стороны
// получают один результат.
func SessionKey(shared []byte, chatID [16]byte, a cipher.Algorithm, keyBits int) ([]byte, error) {
	if len(shared) != SharedSecretSize {
		return nil, ErrInvalidSecret
	}
	if !cipher.ValidKeyBits(keyBits) {
		return nil, cipher.ErrInvalidKeySize
	}
	info := make([]byte, 0, len(sessionKeyLabel)+3)
	info = append(info, sessionKeyLabel...)
	info = append(info, byte(a))
	info = binary.BigEndian.AppendUint16(info, uint16(keyBits))
	return HKDF(shared, chatID[:], info, keyBits/8)
}
