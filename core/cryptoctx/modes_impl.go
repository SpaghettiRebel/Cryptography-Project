package cryptoctx

import (
	"context"
	"fmt"

	"cryptochat/core/modes"
	"cryptochat/core/padding"
)

func (c *CryptoContext) encrypt(ctx context.Context, data []byte) ([]byte, error) {
	in := data
	if c.mode.NeedsPadding() {
		var err error
		if in, err = padding.Pad(data, c.blockSize, c.padding); err != nil {
			return nil, err
		}
	}
	switch c.mode {
	case modes.ECB:
		return c.ecb(ctx, in, c.cipher.EncryptBlock)
	case modes.CBC:
		return c.cbcEncrypt(ctx, in)
	case modes.PCBC:
		return c.pcbcEncrypt(ctx, in)
	case modes.CFB:
		return c.cfbEncrypt(ctx, in)
	case modes.OFB:
		return c.ofb(ctx, in)
	case modes.CTR, modes.RandomDelta:
		return c.counterMode(ctx, in)
	default:
		return nil, fmt.Errorf("cryptoctx: unsupported mode %s", c.mode)
	}
}

func (c *CryptoContext) decrypt(ctx context.Context, data []byte) ([]byte, error) {
	if c.mode.NeedsPadding() && len(data)%c.blockSize != 0 {
		return nil, padding.ErrBadPadding
	}
	var out []byte
	var err error
	switch c.mode {
	case modes.ECB:
		out, err = c.ecb(ctx, data, c.cipher.DecryptBlock)
	case modes.CBC:
		out, err = c.cbcDecrypt(ctx, data)
	case modes.PCBC:
		out, err = c.pcbcDecrypt(ctx, data)
	case modes.CFB:
		out, err = c.cfbDecrypt(ctx, data)
	case modes.OFB:
		out, err = c.ofb(ctx, data)
	case modes.CTR, modes.RandomDelta:
		out, err = c.counterMode(ctx, data)
	default:
		return nil, fmt.Errorf("cryptoctx: unsupported mode %s", c.mode)
	}
	if err != nil {
		return nil, err
	}
	if c.mode.NeedsPadding() {
		return padding.Unpad(out, c.blockSize, c.padding)
	}
	return out, nil
}

// --- ECB ----------------------------------------------------------------------

func (c *CryptoContext) ecb(ctx context.Context, in []byte, op func([]byte) ([]byte, error)) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	err := forEachBlock(ctx, len(in)/bs, c.workers, func(i int) error {
		res, err := op(in[i*bs : (i+1)*bs])
		if err != nil {
			return err
		}
		copy(out[i*bs:], res)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- CBC ----------------------------------------------------------------------
// Cᵢ = E(Pᵢ ⊕ Cᵢ₋₁)
// Pᵢ = D(Cᵢ) ⊕ Cᵢ₋₁

func (c *CryptoContext) cbcEncrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	prev := c.iv
	buf := make([]byte, bs)
	err := forEachBlockSeq(ctx, len(in)/bs, func(i int) error {
		xorInto(buf, in[i*bs:(i+1)*bs], prev)
		res, err := c.cipher.EncryptBlock(buf)
		if err != nil {
			return err
		}
		copy(out[i*bs:], res)
		prev = out[i*bs : (i+1)*bs]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *CryptoContext) cbcDecrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	err := forEachBlock(ctx, len(in)/bs, c.workers, func(i int) error {
		res, err := c.cipher.DecryptBlock(in[i*bs : (i+1)*bs])
		if err != nil {
			return err
		}
		xorInto(out[i*bs:(i+1)*bs], res, c.prevCipherBlock(in, i))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *CryptoContext) prevCipherBlock(ct []byte, i int) []byte {
	if i == 0 {
		return c.iv
	}
	return ct[(i-1)*c.blockSize : i*c.blockSize]
}

// --- PCBC ---------------------------------------------------------------------
// Cᵢ = E(Pᵢ ⊕ Pᵢ₋₁ ⊕ Cᵢ₋₁)
// Pᵢ = D(Cᵢ) ⊕ Pᵢ₋₁ ⊕ Cᵢ₋₁

func (c *CryptoContext) pcbcEncrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	chain := append([]byte(nil), c.iv...)
	buf := make([]byte, bs)
	err := forEachBlockSeq(ctx, len(in)/bs, func(i int) error {
		p := in[i*bs : (i+1)*bs]
		xorInto(buf, p, chain)
		res, err := c.cipher.EncryptBlock(buf)
		if err != nil {
			return err
		}
		copy(out[i*bs:], res)
		xorInto(chain, p, res)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *CryptoContext) pcbcDecrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out, err := c.ecb(ctx, in, c.cipher.DecryptBlock) // out = T_i
	if err != nil {
		return nil, err
	}
	chain := append([]byte(nil), c.iv...)
	for i := 0; i < len(in)/bs; i++ {
		p := out[i*bs : (i+1)*bs]
		xorInto(p, p, chain)
		xorInto(chain, p, in[i*bs:(i+1)*bs])
	}
	return out, nil
}

// --- CFB ----------------------------------------------------------------------
// Cᵢ = Pᵢ ⊕ E(Cᵢ₋₁)
// Pᵢ = Cᵢ ⊕ E(Cᵢ₋₁)

func (c *CryptoContext) cfbEncrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	prev := c.iv
	err := forEachBlockSeq(ctx, ceilDiv(len(in), bs), func(i int) error {
		gamma, err := c.cipher.EncryptBlock(prev)
		if err != nil {
			return err
		}
		end := min((i+1)*bs, len(in))
		xorInto(out[i*bs:end], in[i*bs:end], gamma)
		prev = out[i*bs : end]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *CryptoContext) cfbDecrypt(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	err := forEachBlock(ctx, ceilDiv(len(in), bs), c.workers, func(i int) error {
		gamma, err := c.cipher.EncryptBlock(c.prevCipherBlock(in, i))
		if err != nil {
			return err
		}
		end := min((i+1)*bs, len(in))
		xorInto(out[i*bs:end], in[i*bs:end], gamma)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- OFB ----------------------------------------------------------------------
// Cᵢ = Pᵢ ⊕ Gᵢ, Gᵢ = E(Gᵢ₋₁)
// Pᵢ = Cᵢ ⊕ Gᵢ, Gᵢ = E(Gᵢ₋₁)

func (c *CryptoContext) ofb(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	state := c.iv
	err := forEachBlockSeq(ctx, ceilDiv(len(in), bs), func(i int) error {
		next, err := c.cipher.EncryptBlock(state)
		if err != nil {
			return err
		}
		state = next
		end := min((i+1)*bs, len(in))
		xorInto(out[i*bs:end], in[i*bs:end], state)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- CTR / Random Delta -------------------------------------------------------
// Cᵢ = Pᵢ ⊕ E(ctr₀ + i·Δ)
// Pᵢ = Cᵢ ⊕ E(ctr₀ + i·Δ)

func (c *CryptoContext) counterMode(ctx context.Context, in []byte) ([]byte, error) {
	bs := c.blockSize
	out := make([]byte, len(in))
	err := forEachBlock(ctx, ceilDiv(len(in), bs), c.workers, func(i int) error {
		ctr := make([]byte, bs)
		counterAt(ctr, c.ctr0, c.delta, uint64(i))
		gamma, err := c.cipher.EncryptBlock(ctr)
		if err != nil {
			return err
		}
		end := min((i+1)*bs, len(in))
		xorInto(out[i*bs:end], in[i*bs:end], gamma)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func counterAt(dst, base, delta []byte, i uint64) {
	n := len(dst)
	for k := range dst {
		dst[k] = 0
	}
	for a := 0; a < 8 && a < n; a++ {
		ib := uint32(i >> (8 * uint(a)) & 0xFF)
		if ib == 0 {
			continue
		}
		var carry uint32
		for b := n - 1; b >= a; b-- {
			v := uint32(dst[b-a]) + uint32(delta[b])*ib + carry
			dst[b-a] = byte(v)
			carry = v >> 8
		}
	}
	var carry uint32
	for b := n - 1; b >= 0; b-- {
		v := uint32(dst[b]) + uint32(base[b]) + carry
		dst[b] = byte(v)
		carry = v >> 8
	}
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }
