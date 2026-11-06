package dh

import (
	"errors"
	"math/big"
)

var (
	ErrInvalidModulus   = errors.New("dh: недопустимый модуль")
	ErrNegativeExponent = errors.New("dh: отрицательная степень")
)

var one = big.NewInt(1)

// ModPow — бинарное возведение в степень по модулю, слева направо. Не работает за постоянное время.
func ModPow(b, e, m *big.Int) (*big.Int, error) {
	if m.Sign() <= 0 {
		return nil, ErrInvalidModulus
	}
	if e.Sign() < 0 {
		return nil, ErrNegativeExponent
	}
	if m.Cmp(one) == 0 {
		return new(big.Int), nil
	}
	base := new(big.Int).Mod(b, m)
	result := big.NewInt(1)
	prod := new(big.Int)
	quo := new(big.Int)
	for i := e.BitLen() - 1; i >= 0; i-- {
		prod.Mul(result, result)
		quo.QuoRem(prod, m, result)
		if e.Bit(i) == 1 {
			prod.Mul(result, base)
			quo.QuoRem(prod, m, result)
		}
	}
	return result, nil
}
