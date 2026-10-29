// Package debugprofiler — компонент pprof: профилировщик Go на отладочном порту (куда уходят процессор и память демона).
// Только для разработки: go run ./tools/build -with debug-profiler. С компьютера:
//
//	go tool pprof -top http://адрес-колонки:6701/debug/pprof/profile?seconds=20
package debugprofiler

import (
	"net/http"
	"net/http/pprof"

	"openmediad/internal/components/debug-page"
	"openmediad/internal/core/registry"
)

func init() {
	registry.Register(registry.Component{Name: "debug-profiler"})
	debugpage.AddHandlers(func(mux *http.ServeMux) {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	})
}
