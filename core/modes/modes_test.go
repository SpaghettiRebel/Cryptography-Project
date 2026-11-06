package modes

import "testing"

func TestCipherModes_AllDeclaredAndNamed(t *testing.T) {
	if len(AllCipherModes) != 7 {
		t.Fatalf("expected 7 cipher modes, got %d", len(AllCipherModes))
	}
	seen := map[string]bool{}
	for _, m := range AllCipherModes {
		if !m.IsValid() {
			t.Errorf("%s reported as invalid", m)
		}
		if seen[m.String()] {
			t.Errorf("duplicate name %s", m)
		}
		seen[m.String()] = true
	}
	if CipherMode(200).IsValid() || CipherMode(200).String() != "CipherMode(200)" {
		t.Error("unknown mode must be invalid and have a fallback name")
	}
	if ECB.NeedsIV() || !CBC.NeedsIV() {
		t.Error("only ECB works without IV")
	}
	for _, m := range []CipherMode{ECB, CBC, PCBC} {
		if !m.NeedsPadding() {
			t.Errorf("%s must need padding", m)
		}
	}
	for _, m := range []CipherMode{CFB, OFB, CTR, RandomDelta} {
		if m.NeedsPadding() {
			t.Errorf("%s is a stream mode and must not need padding", m)
		}
	}
}

func TestPaddingModes_AllDeclaredAndNamed(t *testing.T) {
	if len(AllPaddingModes) != 4 {
		t.Fatalf("expected 4 padding modes, got %d", len(AllPaddingModes))
	}
	for _, p := range AllPaddingModes {
		if !p.IsValid() || p.String() == "" {
			t.Errorf("%d must be valid and named", p)
		}
	}
	if PaddingMode(9).IsValid() {
		t.Error("unknown padding must be invalid")
	}
}
