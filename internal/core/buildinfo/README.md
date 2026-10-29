# core/buildinfo — сведения о сборке

Версия и дата (подставляет `tools/build` через `-ldflags -X`), коммит, признак незакоммиченных изменений, теги сборки и цель Go (из
`debug.ReadBuildInfo`). `Line()` и `Build()` — для `openmediad version` и шапки справки.
