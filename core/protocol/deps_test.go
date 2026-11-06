package protocol

import (
	"os/exec"
	"strings"
	"testing"
)

// Пакет описывает только формат и не должен получить код шифров чата.
func TestNoCipherDependency(t *testing.T) {
	if testing.Short() {
		t.Skip("запуск go list")
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, line := range strings.Fields(string(out)) {
		for _, banned := range []string{"core/cipher", "core/algorithms", "core/cryptoctx", "core/modes", "core/padding", "core/message", "core/internal"} {
			if strings.HasSuffix(line, banned) || strings.Contains(line, banned+"/") {
				t.Errorf("запрещённая зависимость: %s", line)
			}
		}
	}
}
