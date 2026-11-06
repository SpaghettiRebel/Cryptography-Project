package modes

import "fmt"

type CipherMode uint8

const (
	ECB CipherMode = iota
	CBC
	PCBC
	CFB
	OFB
	CTR
	RandomDelta
)

var AllCipherModes = []CipherMode{ECB, CBC, PCBC, CFB, OFB, CTR, RandomDelta}

var cipherModeNames = map[CipherMode]string{
	ECB:         "ECB",
	CBC:         "CBC",
	PCBC:        "PCBC",
	CFB:         "CFB",
	OFB:         "OFB",
	CTR:         "CTR",
	RandomDelta: "RandomDelta",
}

func (m CipherMode) String() string {
	if s, ok := cipherModeNames[m]; ok {
		return s
	}
	return fmt.Sprintf("CipherMode(%d)", uint8(m))
}

func (m CipherMode) IsValid() bool {
	_, ok := cipherModeNames[m]
	return ok
}

func (m CipherMode) NeedsIV() bool {
	return m != ECB
}

func (m CipherMode) NeedsPadding() bool {
	switch m {
	case ECB, CBC, PCBC:
		return true
	default:
		return false
	}
}

type PaddingMode uint8

const (
	Zeros PaddingMode = iota
	ANSIX923
	PKCS7
	ISO10126
)

var AllPaddingModes = []PaddingMode{Zeros, ANSIX923, PKCS7, ISO10126}

var paddingModeNames = map[PaddingMode]string{
	Zeros:    "Zeros",
	ANSIX923: "ANSI X.923",
	PKCS7:    "PKCS7",
	ISO10126: "ISO 10126",
}

func (p PaddingMode) String() string {
	if s, ok := paddingModeNames[p]; ok {
		return s
	}
	return fmt.Sprintf("PaddingMode(%d)", uint8(p))
}

func (p PaddingMode) IsValid() bool {
	_, ok := paddingModeNames[p]
	return ok
}
