package padding

import (
	"bytes"
	"errors"
	"testing"

	"cryptochat/core/modes"
)

func TestPad_KnownValues(t *testing.T) {
	data := []byte{0xAA, 0xBB}
	cases := []struct {
		mode modes.PaddingMode
		want []byte
	}{
		{modes.Zeros, []byte{0xAA, 0xBB, 0x00, 0x00}},
		{modes.ANSIX923, []byte{0xAA, 0xBB, 0x00, 0x02}},
		{modes.PKCS7, []byte{0xAA, 0xBB, 0x02, 0x02}},
	}
	for _, tc := range cases {
		got, err := Pad(data, 4, tc.mode)
		if err != nil {
			t.Fatalf("%s: %v", tc.mode, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: Pad = %x, want %x", tc.mode, got, tc.want)
		}
	}

	got, err := Pad(data, 4, modes.ISO10126)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || !bytes.Equal(got[:2], data) || got[3] != 2 {
		t.Errorf("ISO 10126: Pad = %x", got)
	}
}

func TestPad_AlignedInput(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	got, _ := Pad(data, 4, modes.Zeros)
	if !bytes.Equal(got, data) {
		t.Errorf("Zeros on aligned input = %x, want unchanged", got)
	}
	for _, mode := range []modes.PaddingMode{modes.ANSIX923, modes.PKCS7, modes.ISO10126} {
		got, _ := Pad(data, 4, mode)
		if len(got) != 8 || got[7] != 4 {
			t.Errorf("%s on aligned input = %x, want a full extra block", mode, got)
		}
	}
	got, _ = Pad(nil, 4, modes.PKCS7)
	if !bytes.Equal(got, []byte{4, 4, 4, 4}) {
		t.Errorf("PKCS7 on empty input = %x", got)
	}
}

func TestPadUnpad_RoundTrip(t *testing.T) {
	for _, bs := range []int{1, 4, 8, 16} {
		for _, mode := range modes.AllPaddingModes {
			for n := 0; n < 3*bs+1; n++ {
				data := make([]byte, n)
				for i := range data {
					data[i] = byte(i%250 + 1)
				}
				padded, err := Pad(data, bs, mode)
				if err != nil {
					t.Fatalf("%s bs=%d n=%d: %v", mode, bs, n, err)
				}
				if len(padded)%bs != 0 {
					t.Fatalf("%s bs=%d n=%d: padded length %d", mode, bs, n, len(padded))
				}
				back, err := Unpad(padded, bs, mode)
				if err != nil {
					t.Fatalf("%s bs=%d n=%d: unpad: %v", mode, bs, n, err)
				}
				if !bytes.Equal(back, data) {
					t.Fatalf("%s bs=%d n=%d: round trip %x -> %x -> %x", mode, bs, n, data, padded, back)
				}
			}
		}
	}
}

func TestUnpad_ZerosStripsTrailingDataZeros(t *testing.T) {
	padded, _ := Pad([]byte{1, 0, 0}, 4, modes.Zeros)
	back, _ := Unpad(padded, 4, modes.Zeros)
	if !bytes.Equal(back, []byte{1}) {
		t.Errorf("got %x, expected documented behaviour: trailing zeros removed", back)
	}
}

func TestUnpad_Errors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mode modes.PaddingMode
	}{
		{"not multiple of block", []byte{1, 2, 3}, modes.PKCS7},
		{"empty PKCS7", []byte{}, modes.PKCS7},
		{"pad byte zero", []byte{1, 2, 3, 0}, modes.PKCS7},
		{"pad byte too large", []byte{1, 2, 3, 5}, modes.PKCS7},
		{"PKCS7 inconsistent bytes", []byte{1, 2, 3, 2}, modes.PKCS7},
		{"X923 nonzero filler", []byte{1, 2, 7, 2}, modes.ANSIX923},
		{"pad longer than data", []byte{4, 4, 4, 4, 8, 8, 8, 9}, modes.ISO10126},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Unpad(tc.data, 4, tc.mode)
			if !errors.Is(err, ErrBadPadding) {
				t.Fatalf("got %v, want ErrBadPadding", err)
			}
		})
	}
	if _, err := Pad([]byte{1}, 0, modes.PKCS7); err == nil {
		t.Error("block size 0 must be rejected")
	}
	if _, err := Pad([]byte{1}, 4, modes.PaddingMode(77)); err == nil {
		t.Error("unknown padding mode must be rejected")
	}
	if _, err := Unpad([]byte{1, 1, 1, 1}, 4, modes.PaddingMode(77)); err == nil {
		t.Error("unknown padding mode must be rejected")
	}
}

func TestPad_DoesNotAliasInput(t *testing.T) {
	data := make([]byte, 4, 16)
	padded, _ := Pad(data, 4, modes.PKCS7)
	padded[0] = 0xFF
	if data[0] == 0xFF {
		t.Error("Pad returned a slice aliasing the input")
	}
}

// Размеры блока Rijndael: 128, 192 и 256 бит.
func TestPadUnpad_RijndaelBlockSizes(t *testing.T) {
	for _, bs := range []int{16, 24, 32} {
		for _, mode := range modes.AllPaddingModes {
			for _, n := range []int{0, 1, bs - 1, bs, bs + 1} {
				data := bytes.Repeat([]byte{0xA5}, n)
				padded, err := Pad(data, bs, mode)
				if err != nil {
					t.Fatalf("bs=%d %s n=%d: Pad: %v", bs, mode, n, err)
				}
				if len(padded)%bs != 0 {
					t.Fatalf("bs=%d %s n=%d: длина %d не кратна блоку", bs, mode, n, len(padded))
				}
				back, err := Unpad(padded, bs, mode)
				if err != nil || !bytes.Equal(back, data) {
					t.Fatalf("bs=%d %s n=%d: Unpad = %x, %v", bs, mode, n, back, err)
				}
			}
		}
	}
}

// FR-028: в сообщении нет значений байтов данных и различий между причинами.
func TestUnpad_ErrorHasNoDetails(t *testing.T) {
	cases := []struct {
		data []byte
		mode modes.PaddingMode
	}{
		{[]byte{1, 2, 3}, modes.PKCS7},
		{[]byte{}, modes.PKCS7},
		{[]byte{1, 2, 3, 0}, modes.PKCS7},
		{[]byte{1, 2, 3, 5}, modes.ANSIX923},
		{[]byte{1, 2, 3, 2}, modes.PKCS7},
		{[]byte{1, 2, 7, 2}, modes.ANSIX923},
	}
	for _, tc := range cases {
		_, err := Unpad(tc.data, 4, tc.mode)
		if err == nil || err.Error() != ErrBadPadding.Error() {
			t.Errorf("%s %v: ошибка %v, ожидался ровно ErrBadPadding", tc.mode, tc.data, err)
		}
	}
}
