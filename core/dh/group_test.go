package dh

import (
	"math/big"
	"strings"
	"testing"
)

// piFloor возвращает ⌊π·2^bits⌋ по формуле Мэчина с защитными битами.
func piFloor(bits uint) *big.Int {
	const guard = 64
	one := new(big.Int).Lsh(big.NewInt(1), bits+guard)
	atanInv := func(x int64) *big.Int {
		sum := new(big.Int)
		term := new(big.Int).Quo(one, big.NewInt(x))
		x2 := big.NewInt(x * x)
		for k := int64(0); term.Sign() != 0; k++ {
			t := new(big.Int).Quo(term, big.NewInt(2*k+1))
			if k%2 == 0 {
				sum.Add(sum, t)
			} else {
				sum.Sub(sum, t)
			}
			term.Quo(term, x2)
		}
		return sum
	}
	pi := new(big.Int).Mul(big.NewInt(16), atanInv(5))
	pi.Sub(pi, new(big.Int).Mul(big.NewInt(4), atanInv(239)))
	return pi.Rsh(pi, guard)
}

// Формула из RFC 3526, раздел 3: 2^2048 − 2^1984 − 1 + 2^64·(⌊2^1918·π⌋ + 124476).
func TestGroup14Formula(t *testing.T) {
	p := new(big.Int).Lsh(big.NewInt(1), 2048)
	p.Sub(p, new(big.Int).Lsh(big.NewInt(1), 1984))
	p.Sub(p, big.NewInt(1))
	term := piFloor(1918)
	term.Add(term, big.NewInt(124476))
	p.Add(p, term.Lsh(term, 64))

	g := Group14()
	if g.P().Cmp(p) != 0 {
		t.Fatalf("P не совпадает с формулой RFC 3526:\n%x\n%x", g.P(), p)
	}
	if g.G().Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("G = %v", g.G())
	}
	if !strings.HasPrefix(g.P().Text(16), "ffffffffffffffffc90fdaa22168c234") {
		t.Fatalf("начало P: %s", g.P().Text(16)[:32])
	}
	if g.P().BitLen() != 2048 {
		t.Fatalf("длина P: %d", g.P().BitLen())
	}
}

func TestGroupReturnsCopies(t *testing.T) {
	g := Group14()
	g.P().SetInt64(1)
	g.G().SetInt64(1)
	if g.P().BitLen() != 2048 || g.G().Int64() != 2 {
		t.Fatal("значения группы должны быть неизменяемы снаружи")
	}
	a, b := Group14(), Group14()
	a.P()
	if a == b {
		t.Fatal("Group14 должна возвращать новую копию")
	}
}
