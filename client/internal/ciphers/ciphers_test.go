package ciphers

import (
	"slices"
	"testing"

	"cryptochat/core/algorithms"
	"cryptochat/core/cipher"
)

// Ожидание выводится из состава core/algorithms и тега сборки, а не зашито: когда появятся настоящие
// MAGENTA и RC6, тест пройдёт по другой ветке без правок.
func TestInfoFollowsBuild(t *testing.T) {
	for _, a := range []cipher.Algorithm{cipher.MAGENTA, cipher.RC6} {
		info := Info(a)
		real := slices.Contains(algorithms.Available(), a)
		switch {
		case real:
			if !info.Available || info.Substitute || info.Note != "" {
				t.Errorf("%s: настоящая реализация, получено %+v", a, info)
			}
		case DevBuild:
			if !info.Available || !info.Substitute || info.Note == "" {
				t.Errorf("%s: сборка devcipher, получено %+v", a, info)
			}
		default:
			if info.Available || info.Substitute || info.Note == "" {
				t.Errorf("%s: сдаваемая сборка, получено %+v", a, info)
			}
		}
		impl, err := Resolve(a)
		switch {
		case real:
			if err != nil || impl != a {
				t.Errorf("Resolve(%s) = %v, %v", a, impl, err)
			}
		case DevBuild:
			if err != nil || impl == a {
				t.Errorf("Resolve(%s) = %v, %v; ожидалась подстановка", a, impl, err)
			}
			if _, err := algorithms.New(impl, 128); err != nil {
				t.Errorf("подстановка не создаётся: %v", err)
			}
		default:
			if err == nil {
				t.Errorf("Resolve(%s) без реализации успешен", a)
			}
		}
	}
	if Info(cipher.Algorithm(9)).Available {
		t.Fatal("неизвестный алгоритм доступен")
	}
}

func TestOptions(t *testing.T) {
	o := Options()
	if len(o.Algorithms) != 2 || o.Algorithms[0].ID != "MAGENTA" || o.Algorithms[1].ID != "RC6" {
		t.Fatalf("%+v", o.Algorithms)
	}
	for _, a := range o.Algorithms {
		if a.Info != Info(a.Algorithm) {
			t.Errorf("%s: %+v", a.ID, a)
		}
	}
	if !slices.Equal(o.KeyBits, []int{128, 192, 256}) {
		t.Fatalf("%v", o.KeyBits)
	}
	modes := []string{"ECB", "CBC", "PCBC", "CFB", "OFB", "CTR", "RANDOM_DELTA"}
	pads := []string{"ZEROS", "ANSI_X923", "PKCS7", "ISO_10126"}
	check := func(kind string, got []Option, ids []string, warned string) {
		if len(got) != len(ids) {
			t.Fatalf("%s: %d вариантов", kind, len(got))
		}
		for i, opt := range got {
			if opt.ID != ids[i] || opt.Title == "" {
				t.Errorf("%s %d: %+v", kind, i, opt)
			}
			if (opt.Warning != "") != (opt.ID == warned) {
				t.Errorf("%s %s: предупреждение %q", kind, opt.ID, opt.Warning)
			}
			if n, ok := numberOf(kind, opt.ID); !ok || n != i+1 {
				t.Errorf("%s %s: номер enum %d", kind, opt.ID, n)
			}
		}
	}
	check("mode", o.CipherModes, modes, "ECB")
	check("padding", o.Paddings, pads, "ZEROS")
}

func numberOf(kind, id string) (int, bool) {
	if kind == "mode" {
		n, ok := ModeNumber(id)
		return n, ok && ModeID(n) == id
	}
	n, ok := PaddingNumber(id)
	return n, ok && PaddingID(n) == id
}

func TestParse(t *testing.T) {
	if a, ok := AlgorithmByID("RC6"); !ok || a != cipher.RC6 {
		t.Fatal("RC6")
	}
	if a, ok := AlgorithmByID("MAGENTA"); !ok || a != cipher.MAGENTA {
		t.Fatal("MAGENTA")
	}
	for _, bad := range []string{"", "AES", "rc6"} {
		if _, ok := AlgorithmByID(bad); ok {
			t.Errorf("%q принят", bad)
		}
	}
	if _, ok := ModeNumber("XTS"); ok {
		t.Fatal("неизвестный режим принят")
	}
	if ModeID(0) != "" || PaddingID(5) != "" {
		t.Fatal("номер вне enum")
	}
}
