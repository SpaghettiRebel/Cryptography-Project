package dh

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
)

func TestPublicBytesFixedLength(t *testing.T) {
	g := Group14()
	for i := 0; i < 5; i++ {
		k, err := g.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if got := k.PublicBytes(); len(got) != PublicValueSize {
			t.Fatalf("длина %d", len(got))
		}
	}
	// Маленькое значение: без ведущих нулей оно заняло бы один байт.
	small := &KeyPair{group: g, x: big.NewInt(2), y: big.NewInt(5)}
	got := small.PublicBytes()
	if len(got) != PublicValueSize || got[PublicValueSize-1] != 5 || !bytes.Equal(got[:PublicValueSize-1], make([]byte, PublicValueSize-1)) {
		t.Fatalf("ведущие нули потеряны: %x", got)
	}
	back, err := g.ParsePublic(got)
	if err != nil || back.Cmp(big.NewInt(5)) != 0 {
		t.Fatalf("разбор: %v, %v", back, err)
	}
}

func TestParsePublicRejects(t *testing.T) {
	g := Group14()
	encode := func(v *big.Int) []byte { return v.FillBytes(make([]byte, PublicValueSize)) }
	p := g.P()
	cases := map[string][]byte{
		"пусто":    nil,
		"255":      make([]byte, 255),
		"257":      make([]byte, 257),
		"0":        encode(big.NewInt(0)),
		"1":        encode(big.NewInt(1)),
		"p-1":      encode(new(big.Int).Sub(p, big.NewInt(1))),
		"p":        encode(p),
		"p-2":      encode(new(big.Int).Sub(p, big.NewInt(2))),
		"все 0xFF": bytes.Repeat([]byte{0xFF}, PublicValueSize),
	}
	for name, raw := range cases {
		if _, err := g.ParsePublic(raw); !errors.Is(err, ErrInvalidPublicValue) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSharedSecretThroughBytes(t *testing.T) {
	g := Group14()
	a, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	fromA, err := g.ParsePublic(a.PublicBytes())
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := g.ParsePublic(b.PublicBytes())
	if err != nil {
		t.Fatal(err)
	}
	s1, err := a.SharedSecret(fromB)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := b.SharedSecret(fromA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s1, s2) {
		t.Fatal("общий секрет различается")
	}
}

func TestParsePublicAcceptsBounds(t *testing.T) {
	g := Group14()
	// Наибольшее допустимое значение — наибольший квадратичный вычет не больше p − 2.
	top := new(big.Int).Sub(g.P(), big.NewInt(2))
	for big.Jacobi(top, g.P()) != 1 {
		top.Sub(top, big.NewInt(1))
	}
	for _, v := range []*big.Int{big.NewInt(2), top} {
		got, err := g.ParsePublic(v.FillBytes(make([]byte, PublicValueSize)))
		if err != nil || got.Cmp(v) != 0 {
			t.Errorf("граница %v: %v", v, err)
		}
	}
}
