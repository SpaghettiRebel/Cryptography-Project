package dh

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
)

// KeyPair — значения одной стороны. Секретное значение не выводится при форматировании.
type KeyPair struct {
	group *Group
	x, y  *big.Int
}

// GenerateKey создаёт новую пару из crypto/rand.
func (g *Group) GenerateKey() (*KeyPair, error) { return g.generateKey(rand.Reader) }

func (g *Group) generateKey(r io.Reader) (*KeyPair, error) {
	// rand.Int даёт [0, p − 3), после сдвига на 2 получается [2, p − 2].
	x, err := rand.Int(r, new(big.Int).Sub(g.p, big.NewInt(3)))
	if err != nil {
		return nil, fmt.Errorf("dh: источник случайности: %w", err)
	}
	x.Add(x, big.NewInt(2))
	y, err := ModPow(g.g, x, g.p)
	if err != nil {
		return nil, err
	}
	return &KeyPair{group: g, x: x, y: y}, nil
}

// Public возвращает копию открытого значения.
func (k *KeyPair) Public() *big.Int { return new(big.Int).Set(k.y) }

// SharedSecret проверяет значение собеседника и возвращает общий секрет: ровно 256 байт, big-endian.
func (k *KeyPair) SharedSecret(peer *big.Int) ([]byte, error) {
	if err := k.group.ValidatePublic(peer); err != nil {
		return nil, err
	}
	s, err := ModPow(peer, k.x, k.group.p)
	if err != nil {
		return nil, err
	}
	return s.FillBytes(make([]byte, SharedSecretSize)), nil
}

// String не содержит секретного значения.
func (k KeyPair) String() string { return "dh.KeyPair{секрет скрыт}" }

// Format закрывает все глаголы fmt, включая %#v и %d. Приёмник — значение, чтобы копия KeyPair и
// структура с полем этого типа тоже не выводили секрет.
func (k KeyPair) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, k.String()) }

// PrivateBytes возвращает копию секретного значения: ровно 256 байт, big-endian, с ведущими нулями.
// Нужен клиенту, чтобы сохранить значение до публикации и продолжить согласование после перезапуска.
func (k *KeyPair) PrivateBytes() []byte { return k.x.FillBytes(make([]byte, PublicValueSize)) }

// KeyPairFromPrivate восстанавливает пару по сохранённому секретному значению. Значение должно
// занимать ровно 256 байт и лежать в [2, p − 2]; открытое значение вычисляется заново.
func (g *Group) KeyPairFromPrivate(raw []byte) (*KeyPair, error) {
	if len(raw) != PublicValueSize {
		return nil, ErrInvalidPublicValue
	}
	x := new(big.Int).SetBytes(raw)
	if !g.inRange(x) {
		return nil, ErrInvalidPublicValue
	}
	y, err := ModPow(g.g, x, g.p)
	if err != nil {
		return nil, err
	}
	return &KeyPair{group: g, x: x, y: y}, nil
}
