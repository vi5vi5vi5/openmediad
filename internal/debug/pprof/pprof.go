// Package pprof — компонент pprof: профилировщик Go на отладочном порту (куда уходят процессор и память демона).
// Только для разработки: go run ./tools/build -with pprof. С компьютера:
//
//	go tool pprof -top http://адрес-колонки:6701/debug/pprof/profile?seconds=20
package pprof

import (
	"net/http"
	"net/http/pprof"

	"openmediad/internal/debug"
	"openmediad/internal/registry"
)

func init() {
	registry.Register(registry.Component{Name: "pprof"})
	debug.AddHandlers(func(mux *http.ServeMux) {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	})
}
