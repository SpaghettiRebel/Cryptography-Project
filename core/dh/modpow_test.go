package dh

import (
	"errors"
	"math/big"
	"testing"
)

func slowPow(b, e, m int64) int64 {
	r := int64(1) % m
	base := ((b % m) + m) % m
	for i := int64(0); i < e; i++ {
		r = r * base % m
	}
	return r
}

func TestModPow(t *testing.T) {
	for m := int64(1); m <= 40; m++ {
		for b := int64(-5); b <= 45; b += 3 {
			for e := int64(0); e <= 20; e++ {
				got, err := ModPow(big.NewInt(b), big.NewInt(e), big.NewInt(m))
				if err != nil {
					t.Fatal(err)
				}
				if got.Int64() != slowPow(b, e, m) {
					t.Fatalf("%d^%d mod %d = %v, ожидалось %d", b, e, m, got, slowPow(b, e, m))
				}
			}
		}
	}
}

func TestModPowArgumentsUnchanged(t *testing.T) {
	b, e, m := big.NewInt(7), big.NewInt(5), big.NewInt(13)
	if _, err := ModPow(b, e, m); err != nil {
		t.Fatal(err)
	}
	if b.Int64() != 7 || e.Int64() != 5 || m.Int64() != 13 {
		t.Fatal("аргументы изменились")
	}
}

func TestModPowErrors(t *testing.T) {
	if _, err := ModPow(big.NewInt(2), big.NewInt(3), big.NewInt(0)); !errors.Is(err, ErrInvalidModulus) {
		t.Errorf("модуль 0: %v", err)
	}
	if _, err := ModPow(big.NewInt(2), big.NewInt(3), big.NewInt(-7)); !errors.Is(err, ErrInvalidModulus) {
		t.Errorf("отрицательный модуль: %v", err)
	}
	if _, err := ModPow(big.NewInt(2), big.NewInt(-1), big.NewInt(7)); !errors.Is(err, ErrNegativeExponent) {
		t.Errorf("отрицательный показатель: %v", err)
	}
}
