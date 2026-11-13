package daemon

import (
	"fmt"
	"reflect"
)

// Проверка точек расширения. Демон подключает компонент по методам, которые тот реализует (components.go): есть OnReply с нужной подписью —
// компонент получает ответы облака. Обратная сторона: опечатка в подписи (лишний или недостающий аргумент) не даёт ни ошибки сборки, ни
// ошибки запуска — метод просто никогда не вызывается. Поэтому при запуске демон сверяет: если у компонента есть метод с именем точки
// расширения, но подпись у него другая, в журнал идёт предупреждение с ожидаемой подписью.

// hook — точка расширения: имя метода, проверка «подходит» и подпись, которую ждёт демон.
type hook struct {
	name string
	fits func(obj any) bool
	want string
}

var hooks = []hook{
	{"InterceptPhrase", func(o any) bool { _, ok := o.(phraseInterceptor); return ok }, "InterceptPhrase(reqID, text string, fast bool) (handled bool)"},
	{"OnReply", func(o any) bool { _, ok := o.(replyHandler); return ok }, "OnReply(reqID string, directives []string)"},
	{"OnReplyTrack", func(o any) bool { _, ok := o.(trackHandler); return ok }, "OnReplyTrack(reqID, title, artist string)"},
	{"OnDirective", func(o any) bool { _, ok := o.(directiveHandler); return ok }, "OnDirective(name string)"},
	{"OnFocus", func(o any) bool { _, ok := o.(focusHandler); return ok }, "OnFocus(on bool)"},
	{"StatusLine", func(o any) bool { _, ok := o.(statusLiner); return ok }, "StatusLine() string"},
	{"DebugState", func(o any) bool { _, ok := o.(debugStater); return ok }, "DebugState() any"},
	{"Reload", func(o any) bool { _, ok := o.(configReloader); return ok }, "Reload(cfg any) (restart []string)"},
	{"Shutdown", func(o any) bool { _, ok := o.(shutdowner); return ok }, "Shutdown()"},
	{"LogsDialog", func(o any) bool { _, ok := o.(dialogLogger); return ok }, "LogsDialog() bool"},
	{"OnArrival", func(o any) bool { _, ok := o.(arrivalNotifier); return ok }, "OnArrival(func())"},
}

// hookMismatches — методы компонента, названные как точки расширения, но с другой подписью: демон их не вызовет.
func hookMismatches(obj any) []string {
	if obj == nil {
		return nil
	}
	t := reflect.TypeOf(obj)
	var out []string
	for _, h := range hooks {
		m, ok := t.MethodByName(h.name)
		if !ok || h.fits(obj) {
			continue
		}
		out = append(out, fmt.Sprintf("метод %s имеет подпись %s, а демон ждёт %s — он не будет вызываться", h.name, signature(m), h.want))
	}
	return out
}

// signature — подпись метода без получателя: «func(string, string) bool».
func signature(m reflect.Method) string {
	ft := m.Type
	s := "func("
	for i := 1; i < ft.NumIn(); i++ { // 0 — получатель
		if i > 1 {
			s += ", "
		}
		s += ft.In(i).String()
	}
	s += ")"
	switch ft.NumOut() {
	case 0:
	case 1:
		s += " " + ft.Out(0).String()
	default:
		s += " ("
		for i := 0; i < ft.NumOut(); i++ {
			if i > 0 {
				s += ", "
			}
			s += ft.Out(i).String()
		}
		s += ")"
	}
	return s
}
