//go:build !devcipher

package algorithms

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"cryptochat/core/cipher"
)

func TestUnavailable(t *testing.T) {
	for _, a := range []cipher.Algorithm{cipher.MAGENTA, cipher.RC6, 255} {
		if _, err := New(a, 128); !errors.Is(err, cipher.ErrAlgorithmUnavailable) {
			t.Errorf("%s: %v, ожидалась ErrAlgorithmUnavailable", a, err)
		}
	}
	if got := Available(); len(got) != 0 {
		t.Errorf("Available() = %v, ожидался пустой список", got)
	}
}

// Шаблон указывает весь модуль: тест запускается из папки пакета, и ./... охватил бы только её.
// Сам пакет stubcipher входит в шаблон, поэтому проверяются зависимости остальных пакетов.
func TestNoStubInReleaseBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("запуск go list")
	}
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}: {{join .Deps " "}}`, "cryptochat/core/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, ok := strings.Cut(line, ": ")
		if !ok {
			pkg, deps, _ = strings.Cut(line, ":")
		}
		if strings.HasSuffix(pkg, "internal/stubcipher") {
			continue
		}
		checked++
		if strings.Contains(deps, "stubcipher") {
			t.Errorf("тестовый шифр попал в зависимости %s без тега devcipher", pkg)
		}
	}
	if checked < 8 {
		t.Fatalf("проверено всего %d пакетов:\n%s", checked, out)
	}
}
