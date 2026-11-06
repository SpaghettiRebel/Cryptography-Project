package dh

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"cryptochat/core/cipher"
	"cryptochat/core/kdf"
)

func TestAgreement(t *testing.T) {
	runs := 1000
	if testing.Short() {
		runs = 10
	}
	g := Group14()
	seen := map[string]bool{}
	for i := 0; i < runs; i++ {
		start := time.Now()
		a, err := g.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		b, err := g.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		sa, err := a.SharedSecret(b.Public())
		if err != nil {
			t.Fatal(err)
		}
		// Две стороны в одном потоке: на одну приходится половина времени (SC-010).
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("согласование заняло %v", d)
		}
		sb, err := b.SharedSecret(a.Public())
		if err != nil {
			t.Fatal(err)
		}
		if len(sa) != SharedSecretSize || !bytes.Equal(sa, sb) {
			t.Fatalf("секреты различаются: длины %d и %d", len(sa), len(sb))
		}
		pub := a.Public().Text(16)
		if seen[pub] {
			t.Fatal("значение повторилось")
		}
		seen[pub] = true
	}
}

func TestValidatePublic(t *testing.T) {
	g := Group14()
	p := g.P()
	bad := map[string]*big.Int{
		"0":   big.NewInt(0),
		"1":   big.NewInt(1),
		"p-1": new(big.Int).Sub(p, big.NewInt(1)),
		"p":   p,
		"p+1": new(big.Int).Add(p, big.NewInt(1)),
		"-5":  big.NewInt(-5),
		"nil": nil,
	}
	k, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for name, y := range bad {
		if err := g.ValidatePublic(y); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := k.SharedSecret(y); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("SharedSecret(%s): %v", name, err)
		}
	}
	for name, y := range map[string]*big.Int{"2": big.NewInt(2), "p-2": new(big.Int).Sub(p, big.NewInt(2))} {
		if err := g.ValidatePublic(y); err != nil {
			t.Errorf("%s должно быть допустимо: %v", name, err)
		}
	}
}

func TestSecretNotPrinted(t *testing.T) {
	k, err := Group14().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secretDec, secretHex := k.x.String(), k.x.Text(16)
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x"} {
		for what, value := range map[string]any{"указатель": k, "копия": *k, "поле": struct{ K KeyPair }{*k}} {
			out := fmt.Sprintf(format, value)
			if strings.Contains(out, secretDec) || strings.Contains(strings.ToLower(out), secretHex) || strings.Contains(out, "false [") {
				t.Errorf("%s, %s: выводит секретное значение: %s", format, what, out)
			}
		}
	}
	if s := k.String(); strings.Contains(s, secretDec) {
		t.Error("String выводит секретное значение")
	}
}

func TestPublicIsCopy(t *testing.T) {
	k, err := Group14().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := k.Public().Text(16)
	k.Public().SetInt64(1)
	if k.Public().Text(16) != want {
		t.Fatal("Public должен возвращать копию")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("источник отказал")
}

func TestRandomSourceFailure(t *testing.T) {
	if _, err := Group14().generateKey(failingReader{}); err == nil {
		t.Fatal("отказ источника случайности должен возвращаться")
	}
}

func TestLeadingZerosKeepLength(t *testing.T) {
	// Секрет с ведущими нулями: g^x, где x мал, даёт короткое число.
	g := Group14()
	k := &KeyPair{group: g, x: big.NewInt(3), y: big.NewInt(8)}
	s, err := k.SharedSecret(big.NewInt(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != SharedSecretSize || s[SharedSecretSize-1] != 8 || s[0] != 0 {
		t.Fatalf("длина %d, конец %d", len(s), s[len(s)-1])
	}
}

// Две стороны проходят весь путь до сеансового ключа; размеры секрета в dh и kdf согласованы.
func TestAgreementToSessionKey(t *testing.T) {
	if SharedSecretSize != kdf.SharedSecretSize {
		t.Fatalf("размер секрета: dh %d, kdf %d", SharedSecretSize, kdf.SharedSecretSize)
	}
	g := Group14()
	a, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sa, err := a.SharedSecret(b.Public())
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.SharedSecret(a.Public())
	if err != nil {
		t.Fatal(err)
	}
	chat := [16]byte{1}
	ka, err := kdf.SessionKey(sa, chat, cipher.RC6, 256)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := kdf.SessionKey(sb, chat, cipher.RC6, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ka, kb) {
		t.Fatal("стороны получили разные ключи")
	}
}
