package cli

import (
	"os"
	"path/filepath"
	"testing"

	"openmediad/internal/core/platform/layout"
	"openmediad/internal/core/registry"
)

// Требования компонентов к прошивке проверяются по файлам корня колонки (layout.Root); непроверяемые пропускаются.
func TestCheckNeed(t *testing.T) {
	old := layout.Root
	layout.Root = t.TempDir()
	defer func() { layout.Root = old }()

	if present, known := checkNeed(registry.NeedArecord); !known || present {
		t.Fatalf("arecord: известно %v, есть %v — ждали «известно, нет»", known, present)
	}
	p := filepath.Join(layout.Root, "usr", "bin", "arecord")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, nil, 0o755)
	if present, known := checkNeed(registry.NeedArecord); !known || !present {
		t.Fatalf("arecord: известно %v, есть %v — ждали «известно, есть»", known, present)
	}
	if _, known := checkNeed(registry.NeedNetwork); known {
		t.Fatal("сеть проверять нечем: требование только перечисляется")
	}
	if _, known := checkNeed("нет-такого"); known {
		t.Fatal("неизвестное требование — не проверяется")
	}
}
