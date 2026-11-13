package daemon

import (
	"strings"
	"testing"
)

// oldStyle — компонент, написанный по старой документации: InterceptPhrase без третьего аргумента, OnFocus без аргумента.
type oldStyle struct{}

func (oldStyle) InterceptPhrase(reqID, text string) bool { return false }
func (oldStyle) OnFocus()                                {}
func (oldStyle) OnReply(string, []string)                {} // эта подпись верная
func (oldStyle) Helper(int) string                       { return "" }

type goodStyle struct{}

func (goodStyle) InterceptPhrase(reqID, text string, fast bool) bool { return false }
func (goodStyle) OnFocus(bool)                                       {}
func (goodStyle) StatusLine() string                                 { return "" }

// Метод с именем точки расширения и чужой подписью демон не вызовет: об этом нужно сказать, а не молчать.
func TestHookMismatchesAreReported(t *testing.T) {
	got := hookMismatches(oldStyle{})
	if len(got) != 2 {
		t.Fatalf("ждали два предупреждения, получили %q", got)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"InterceptPhrase", "func(string, string) bool", "fast bool", "OnFocus", "OnFocus(on bool)"} {
		if !strings.Contains(all, want) {
			t.Errorf("в предупреждениях нет %q:\n%s", want, all)
		}
	}
	if got := hookMismatches(goodStyle{}); len(got) != 0 {
		t.Fatalf("верные подписи предупреждений не дают: %q", got)
	}
	if got := hookMismatches(&goodStyle{}); len(got) != 0 {
		t.Fatalf("указатель: %q", got)
	}
	if hookMismatches(nil) != nil || len(hookMismatches(struct{}{})) != 0 {
		t.Fatal("пустой объект предупреждений не даёт")
	}
}
