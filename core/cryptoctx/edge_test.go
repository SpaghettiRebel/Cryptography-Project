package cryptoctx

import (
	"bytes"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"cryptochat/core/modes"
	"cryptochat/core/padding"
)

var blockModes = []modes.CipherMode{modes.ECB, modes.CBC, modes.PCBC}
var streamModes = []modes.CipherMode{modes.CFB, modes.OFB, modes.CTR, modes.RandomDelta}

func TestZerosLimitation(t *testing.T) {
	data := []byte{1, 2, 3, 0, 0}
	for _, mode := range blockModes {
		c := newCtx(t, mode, modes.Zeros)
		ct, _ := c.Encrypt(data)
		pt, err := c.Decrypt(ct)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(pt, data[:3]) {
			t.Errorf("%s: %x; ожидалась известная потеря нулей в конце", mode, pt)
		}
	}
	for _, mode := range streamModes {
		c := newCtx(t, mode, modes.Zeros)
		ct, _ := c.Encrypt(data)
		pt, err := c.Decrypt(ct)
		if err != nil || !bytes.Equal(pt, data) {
			t.Errorf("%s: в поточном режиме нули должны сохраниться: %x, %v", mode, pt, err)
		}
	}
}

func TestBadCiphertext(t *testing.T) {
	for _, mode := range blockModes {
		c := newCtx(t, mode, modes.PKCS7)
		if _, err := c.Decrypt(make([]byte, 17)); err != padding.ErrBadPadding {
			t.Errorf("%s: длина, не кратная блоку: %v, ожидалась ровно ErrBadPadding", mode, err)
		}
	}
	// ECB с нарушенной структурой набивки: блок специально собран из известного открытого текста.
	for _, pad := range []modes.PaddingMode{modes.ANSIX923, modes.PKCS7, modes.ISO10126} {
		c := newCtx(t, modes.ECB, pad)
		noPad := newCtx(t, modes.ECB, modes.Zeros)
		bad := bytes.Repeat([]byte{0xEE}, 16) // последний байт 0xEE > размера блока
		ct, err := noPad.Encrypt(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Decrypt(ct); !errors.Is(err, padding.ErrBadPadding) {
			t.Errorf("%s: ошибка %v, ожидалась ErrBadPadding", pad, err)
		}
	}
	// Zeros не может обнаружить неверные данные.
	zeros := newCtx(t, modes.ECB, modes.Zeros)
	if _, err := zeros.Decrypt(randomBytes(t, 32)); err != nil {
		t.Errorf("Zeros не должен давать ошибку на произвольных данных: %v", err)
	}

	// Неверный ключ или IV: паники нет, результат — ошибка набивки или другие данные.
	data := randomBytes(t, 100)
	for _, mode := range modes.AllCipherModes {
		for _, pad := range modes.AllPaddingModes {
			c := newCtx(t, mode, pad)
			ct, err := c.Encrypt(data)
			if err != nil {
				t.Fatal(err)
			}
			wrongKey, err := New(newStub(t), randomBytes(t, 16), mode, pad, ivOption(mode, c.IV())...)
			if err != nil {
				t.Fatal(err)
			}
			if pt, err := wrongKey.Decrypt(ct); err == nil && bytes.Equal(pt, data) {
				t.Errorf("%s/%s: неверный ключ восстановил данные", mode, pad)
			} else if err != nil && !errors.Is(err, padding.ErrBadPadding) {
				t.Errorf("%s/%s: неожиданная ошибка: %v", mode, pad, err)
			}
			if !mode.NeedsIV() {
				continue
			}
			wrongIV, err := New(newStub(t), testKey, mode, pad, WithIV(randomBytes(t, 16)))
			if err != nil {
				t.Fatal(err)
			}
			if pt, err := wrongIV.Decrypt(ct); err == nil && bytes.Equal(pt, data) {
				t.Errorf("%s/%s: неверный IV восстановил данные", mode, pad)
			} else if err != nil && !errors.Is(err, padding.ErrBadPadding) {
				t.Errorf("%s/%s: неожиданная ошибка: %v", mode, pad, err)
			}
		}
	}
}

// FR-028: тексты ошибок не содержат ключа, IV и данных. Ищется вся строка целиком, а не отдельные
// байты: иначе цифры длин в сообщениях давали бы ложные срабатывания.
func TestErrorsHaveNoSecrets(t *testing.T) {
	key := randomBytes(t, 16)
	iv := randomBytes(t, 16)
	data := randomBytes(t, 32)
	secrets := []string{
		hex.EncodeToString(key), strings.ToUpper(hex.EncodeToString(key)),
		hex.EncodeToString(iv), strings.ToUpper(hex.EncodeToString(iv)),
		hex.EncodeToString(data), strings.ToUpper(hex.EncodeToString(data)),
	}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
			return
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: сообщение содержит секрет: %v", name, err)
			}
		}
	}
	c, err := New(newStub(t), key, modes.CBC, modes.PKCS7, WithIV(iv))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Decrypt(data[:17])
	check("длина шифротекста", err)
	_, err = New(newStub(t), key[:5], modes.CBC, modes.PKCS7)
	check("длина ключа", err)
	_, err = New(newStub(t), key, modes.CBC, modes.PKCS7, WithIV(iv[:5]))
	check("длина IV", err)
	block := newStub(t)
	_, err = block.EncryptBlock(data[:16])
	check("нет ключа", err)
	if err := block.SetKey(key); err != nil {
		t.Fatal(err)
	}
	_, err = block.EncryptBlock(data[:5])
	check("длина блока", err)

	// Ошибка набивки точно равна ErrBadPadding: в ней нет расшифрованных байтов.
	noPad := newCtx(t, modes.ECB, modes.Zeros)
	ct, _ := noPad.Encrypt(bytes.Repeat([]byte{0xEE}, 16))
	_, err = newCtx(t, modes.ECB, modes.PKCS7).Decrypt(ct)
	if err == nil || err.Error() != padding.ErrBadPadding.Error() {
		t.Errorf("ошибка набивки: %v", err)
	}
	dir := t.TempDir()
	check("входной файл", c.EncryptFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out")))
}
