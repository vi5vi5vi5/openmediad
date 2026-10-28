// Package buildinfo — сведения о сборке: версия, коммит, дата, цель и теги.
//
// Версию и дату подставляет tools/build через -ldflags "-X openmediad/internal/buildinfo.Version=…". Коммит, признак
// незакоммиченных изменений, теги и цель Go кладёт в бинарник сам (debug.ReadBuildInfo), если сборка шла из git.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Задаются при сборке через -ldflags -X.
var (
	Version = "0.0.0-dev"
	Date    = "" // ГГГГ-ММ-ДД; пусто — берётся время коммита
)

// Info — всё, что известно о сборке.
type Info struct {
	Version   string
	Commit    string // короткий хэш коммита; пусто — сборка не из git
	Modified  bool   // в рабочей копии были незакоммиченные изменения
	Date      string // ДД.ММ.ГГГГ
	GoVersion string
	Target    string   // ОС/архитектура, например linux/arm64
	Tags      []string // теги сборки (-tags)
}

// Get собирает сведения о текущем бинарнике.
func Get() Info {
	i := Info{Version: Version, GoVersion: runtime.Version(), Target: runtime.GOOS + "/" + runtime.GOARCH}
	var vcsTime string
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.GoVersion != "" {
			i.GoVersion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				i.Commit = s.Value[:min(7, len(s.Value))]
			case "vcs.modified":
				i.Modified = s.Value == "true"
			case "vcs.time":
				vcsTime = s.Value
			case "-tags":
				i.Tags = SplitTags(s.Value)
			}
		}
	}
	i.Date = humanDate(Date, vcsTime)
	return i
}

// SplitTags разбирает значение -tags: теги через запятую (так пишет go build) или через пробел (старый формат).
func SplitTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// humanDate переводит дату в вид ДД.ММ.ГГГГ: сначала заданную при сборке, иначе время коммита.
func humanDate(set, vcs string) string {
	for _, v := range []string{set, vcs} {
		if v == "" {
			continue
		}
		for _, layout := range []string{"2006-01-02", time.RFC3339} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.Format("02.01.2006")
			}
		}
		return v
	}
	return ""
}

// Line — одна строка для `openmediad version` и шапки помощи.
func (i Info) Line() string {
	var extra []string
	if i.Commit != "" {
		c := "коммит " + i.Commit
		if i.Modified {
			c += " с изменениями"
		}
		extra = append(extra, c)
	}
	if i.Date != "" {
		extra = append(extra, i.Date)
	}
	s := "openmediad " + i.Version
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

// Build — строка о том, чем и под что собрано.
func (i Info) Build() string {
	tags := "нет"
	if len(i.Tags) > 0 {
		tags = strings.Join(i.Tags, " ")
	}
	return fmt.Sprintf("сборка: %s, %s, теги: %s", i.GoVersion, i.Target, tags)
}
