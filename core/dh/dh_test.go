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
	for name, y := range map[string]*big.Int{"2": big.NewInt(2), "4": big.NewInt(4), "открытое значение": k.Public()} {
		if err := g.ValidatePublic(y); err != nil {
			t.Errorf("%s должно быть допустимо: %v", name, err)
		}
	}
}

// Значение в диапазоне, но вне подгруппы порядка q отвергается: p − 2 имеет порядок 2q, квадратичный
// невычет не является степенью g. Честные открытые значения проходят.
func TestValidatePublicSubgroup(t *testing.T) {
	g := Group14()
	p := g.P()
	q := new(big.Int).Rsh(p, 1)
	pm2 := new(big.Int).Sub(p, big.NewInt(2))
	if r, _ := ModPow(pm2, q, p); r.Cmp(big.NewInt(1)) == 0 {
		t.Fatal("p − 2 оказался в подгруппе порядка q")
	}
	// Наименьший квадратичный невычет по символу Якоби (для простого p совпадает с символом Лежандра).
	nonResidue := big.NewInt(3)
	for big.Jacobi(nonResidue, p) != -1 {
		nonResidue.Add(nonResidue, big.NewInt(1))
	}
	// Невычет, умноженный на открытое значение, тоже вне подгруппы.
	k, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	mixed := new(big.Int).Mul(k.Public(), nonResidue)
	mixed.Mod(mixed, p)
	for name, y := range map[string]*big.Int{"p-2": pm2, "невычет " + nonResidue.String(): nonResidue, "y·невычет": mixed} {
		if err := g.ValidatePublic(y); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := g.ParsePublic(y.FillBytes(make([]byte, PublicValueSize))); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("ParsePublic(%s): %v", name, err)
		}
		if _, err := k.SharedSecret(y); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("SharedSecret(%s): %v", name, err)
		}
	}
	for i := 0; i < 20; i++ {
		other, err := g.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ParsePublic(other.PublicBytes()); err != nil {
			t.Fatalf("честное открытое значение отвергнуто: %v", err)
		}
		if _, err := k.SharedSecret(other.Public()); err != nil {
			t.Fatalf("общий секрет с честным значением: %v", err)
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

// Пара, восстановленная по сохранённому секретному значению, совпадает с исходной.
func TestKeyPairFromPrivate(t *testing.T) {
	g := Group14()
	a, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	raw := a.PrivateBytes()
	if len(raw) != 256 {
		t.Fatalf("длина секретного значения %d", len(raw))
	}
	if new(big.Int).SetBytes(raw).Cmp(a.x) != 0 {
		t.Fatal("PrivateBytes не равен секретному значению")
	}
	r, err := g.KeyPairFromPrivate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.PublicBytes(), a.PublicBytes()) {
		t.Fatal("восстановленная пара даёт другое открытое значение")
	}
	s1, err := a.SharedSecret(b.Public())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := r.SharedSecret(b.Public())
	if err != nil {
		t.Fatal(err)
	}
	s3, err := b.SharedSecret(r.Public())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) || !bytes.Equal(s1, s3) {
		t.Fatal("общий секрет восстановленной пары отличается")
	}
	// Срез — копия: его изменение не трогает пару.
	raw[0] ^= 0xff
	if !bytes.Equal(r.PublicBytes(), a.PublicBytes()) || new(big.Int).SetBytes(a.PrivateBytes()).Cmp(a.x) != 0 {
		t.Fatal("PrivateBytes должен возвращать копию")
	}
	// Короткое значение дополняется ведущими нулями до 256 байт.
	small := &KeyPair{group: g, x: big.NewInt(5), y: big.NewInt(32)}
	if p := small.PrivateBytes(); len(p) != 256 || p[255] != 5 || p[0] != 0 {
		t.Fatal("PrivateBytes без ведущих нулей")
	}
	k5, err := g.KeyPairFromPrivate(small.PrivateBytes())
	if err != nil || k5.Public().Cmp(big.NewInt(32)) != 0 {
		t.Fatalf("2^5 mod p: %v", err)
	}
}

func TestKeyPairFromPrivateRejects(t *testing.T) {
	g := Group14()
	p := g.P()
	one := new(big.Int).Lsh(big.NewInt(1), 2048)
	bad := map[string][]byte{
		"0":          make([]byte, 256),
		"1":          big.NewInt(1).FillBytes(make([]byte, 256)),
		"p-1":        new(big.Int).Sub(p, big.NewInt(1)).FillBytes(make([]byte, 256)),
		"p":          p.FillBytes(make([]byte, 256)),
		"2^2048-1":   new(big.Int).Sub(one, big.NewInt(1)).FillBytes(make([]byte, 256)),
		"255 байт":   make([]byte, 255),
		"257 байт":   append([]byte{0}, big.NewInt(7).FillBytes(make([]byte, 256))...),
		"пусто":      nil,
		"1 байт (5)": {5},
	}
	for name, raw := range bad {
		if _, err := g.KeyPairFromPrivate(raw); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, x := range map[string]*big.Int{"2": big.NewInt(2), "p-2": new(big.Int).Sub(p, big.NewInt(2))} {
		if _, err := g.KeyPairFromPrivate(x.FillBytes(make([]byte, 256))); err != nil {
			t.Errorf("%s должно быть допустимо: %v", name, err)
		}
	}
}

// Восстановленная пара тоже не выводит секрет при форматировании.
func TestRestoredSecretNotPrinted(t *testing.T) {
	g := Group14()
	a, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.KeyPairFromPrivate(a.PrivateBytes())
	if err != nil {
		t.Fatal(err)
	}
	secretDec, secretHex := r.x.String(), r.x.Text(16)
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x"} {
		for what, value := range map[string]any{"указатель": r, "копия": *r} {
			out := fmt.Sprintf(format, value)
			if strings.Contains(out, secretDec) || strings.Contains(strings.ToLower(out), secretHex) {
				t.Errorf("%s, %s: выводит секретное значение", format, what)
			}
		}
	}
}
