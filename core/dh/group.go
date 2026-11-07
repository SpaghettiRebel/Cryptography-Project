// Пакет dh реализует протокол Диффи-Хеллмана на группе 14 RFC 3526.
package dh

import (
	"errors"
	"math/big"
)

// ErrInvalidPublicValue — открытое значение вне диапазона [2, p − 2] или вне подгруппы порядка q.
var ErrInvalidPublicValue = errors.New("dh: недопустимое открытое значение")

// SharedSecretSize — размер общего секрета в байтах: длина p.
const SharedSecretSize = 256

// group14Prime — простое 2048 бит из RFC 3526, раздел 3; g = 2.
const group14Prime = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D" +
	"C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F" +
	"83655D23DCA3AD961C62F356208552BB9ED529077096966D" +
	"670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9" +
	"DE2BCBF6955817183995497CEA956AE515D2261898FA0510" +
	"15728E5A8AACAA68FFFFFFFFFFFFFFFF"

// Group — параметры группы. Значения неизменяемы снаружи.
type Group struct {
	p, g, q *big.Int
}

// Group14 возвращает новую копию параметров группы 14.
func Group14() *Group {
	p, ok := new(big.Int).SetString(group14Prime, 16)
	if !ok {
		panic("dh: некорректная константа группы")
	}
	// p — безопасное простое: q = (p − 1)/2 тоже простое, и g = 2 порождает подгруппу порядка q.
	q := new(big.Int).Rsh(p, 1)
	return &Group{p: p, g: big.NewInt(2), q: q}
}

// P возвращает копию простого модуля.
func (g *Group) P() *big.Int { return new(big.Int).Set(g.p) }

// G возвращает копию генератора.
func (g *Group) G() *big.Int { return new(big.Int).Set(g.g) }

// inRange сообщает, что 2 ≤ y ≤ p − 2.
func (g *Group) inRange(y *big.Int) bool {
	if y == nil || y.Cmp(big.NewInt(2)) < 0 {
		return false
	}
	return y.Cmp(new(big.Int).Sub(g.p, big.NewInt(2))) <= 0
}

// ValidatePublic допускает только значения 2 ≤ y ≤ p − 2, лежащие в подгруппе простого порядка q,
// которую порождает g: y^q mod p = 1. Значение вне подгруппы (например, p − 2 порядка 2q или любой
// квадратичный невычет) раскрывало бы чётность секрета собеседника.
func (g *Group) ValidatePublic(y *big.Int) error {
	if !g.inRange(y) {
		return ErrInvalidPublicValue
	}
	r, err := ModPow(y, g.q, g.p)
	if err != nil || r.Cmp(one) != 0 {
		return ErrInvalidPublicValue
	}
	return nil
}
