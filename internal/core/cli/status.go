package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/platform/proc"
)

var statusCmd = Command{
	Name:    "status",
	Summary: "работает ли движок, что играет сам, память, соединения",
	Usage:   "[-v] [-json]",
	Help: `Состояние движка: версия, режим, сколько работает, что openmediad играет сам (остальное играет штатный mediad),
соединения платформы через шим (норма — 4), память демона вместе с его aplay и декодерами, компоненты.

С -v — ещё плееры автомата mediad и треки движка. Код выхода 1, если демон не запущен.`,
	Run: func(c *Ctx, args []string) error {
		fs := flags(c, "status")
		verbose := fs.Bool("v", false, "подробно: плееры и треки")
		asJSON := fs.Bool("json", false, "ответ демона как есть (JSON)")
		if err := parse(fs, args, 0); err != nil {
			return err
		}
		var st ctlsock.Status
		err := ctlsock.Call(ctlsock.SocketPath(), ctlsock.Request{Cmd: "status", Detail: *verbose || *asJSON}, &st)
		if *asJSON && err == nil {
			b, _ := json.MarshalIndent(st, "", "  ")
			c.Printf("%s\n", b)
			return nil
		}
		for _, line := range systemStatus() {
			c.Printf("%s\n", line)
		}
		if errors.Is(err, ctlsock.ErrNotRunning) {
			c.Printf("демон:      не запущен\n")
			return errSilent
		}
		if err != nil {
			return err
		}
		printStatus(c, st, *verbose)
		return nil
	},
}

func printStatus(c *Ctx, st ctlsock.Status, verbose bool) {
	c.Printf("демон:      работает %s, pid %d, режим %s\n", humanDuration(time.Duration(st.UptimeS)*time.Second), st.PID, st.Mode)
	c.Printf("версия:     %s\n", st.Version)
	c.Printf("настройки:  %s\n", st.Config)
	var own []string
	if st.Music {
		own = append(own, "музыку Яндекса")
	}
	if st.Voice {
		own = append(own, "голос Алисы")
	}
	if st.Sounds {
		own = append(own, "звонки и системные звуки")
	}
	if len(own) == 0 {
		c.Printf("играет сам: ничего — прозрачный посредник, всё играет штатный mediad\n")
	} else {
		c.Printf("играет сам: %s; остальное — штатный mediad\n", strings.Join(own, ", "))
	}
	conns := fmt.Sprintf("%d соединения платформы через шим", st.Connections)
	if st.Connections != 4 {
		conns += " (норма — 4: платформа ещё подключается или правило iptables не стоит)"
	}
	c.Printf("шина:       %s\n", conns)
	c.Printf("память:     %s\n", memoryLine(st.PID, st.HeapKB))
	if st.Stock != nil {
		c.Printf("штатный:    %s\n", stockLine(*st.Stock))
	}
	var comps []string
	for _, cs := range st.Components {
		switch {
		case cs.Error != "":
			comps = append(comps, cs.Name+" (ошибка: "+cs.Error+")")
		case cs.Running:
			comps = append(comps, cs.Name)
		case !cs.Enabled:
			comps = append(comps, cs.Name+" (выключен)")
		}
	}
	c.Printf("компоненты: %s\n", listOrNone(comps))
	for _, cs := range st.Components { // строки о себе, которые пишут сами компоненты
		if cs.Line != "" {
			c.Printf("%-11s %s\n", cs.Name+":", cs.Line)
		}
	}
	c.Printf("журнал:     уровень %s (openmediad logs)\n", st.LogLevel)
	for _, w := range st.Warnings {
		c.Printf("внимание:   %s\n", w)
	}
	if verbose {
		if st.Stock != nil {
			printStock(c, *st.Stock)
		}
		printJSON(c, "плееры", st.Players)
		names := make([]string, 0, len(st.Details))
		for n := range st.Details {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			printJSON(c, n, st.Details[n])
		}
	}
}

// stockLine — одной строкой: что штатный mediad сыграл сам с запуска демона. Это и есть то, чего движку не хватает до работы без него.
func stockLine(s ctlsock.StockUse) string {
	since := humanDuration(time.Duration(s.SinceS) * time.Second)
	if len(s.Played) == 0 {
		line := "mediad сам ничего не играл за " + since
		if len(s.Returned) > 0 {
			line += "; движок возвращал ему команды (status -v)"
		}
		return line
	}
	parts := make([]string, 0, len(s.Played))
	for _, p := range s.Played {
		parts = append(parts, fmt.Sprintf("%s ×%d (%s назад)", p.What, p.N, humanDuration(time.Duration(p.AgoS)*time.Second)))
	}
	return "mediad сам сыграл за " + since + ": " + strings.Join(parts, ", ")
}

// printStock — подробно: что сыграл штатный mediad, что движок ему вернул и что пропустил.
func printStock(c *Ctx, s ctlsock.StockUse) {
	section := func(title string, list []ctlsock.StockCount) {
		if len(list) == 0 {
			return
		}
		c.Printf("%s\n", title)
		for _, x := range list {
			c.Printf("  %-34s ×%-6d %s назад\n", x.What, x.N, humanDuration(time.Duration(x.AgoS)*time.Second))
		}
	}
	section("штатный mediad сам сыграл (этого движок пока не умеет):", s.Played)
	section("команды, которые движок взял, но вернул штатному mediad (не смог до первого звука):", s.Returned)
	section("команды платформы, которые движок пропустил штатному mediad:", s.Passed)
}

// memoryLine — Pss демона и его потомков (aplay, maind --gst-launch): сколько движок на самом деле стоит колонке.
func memoryLine(pid int, heapKB uint64) string {
	self, err := proc.PssKB(pid)
	if err != nil {
		return fmt.Sprintf("куча Go %s (Pss недоступен: %v)", kbString(int64(heapKB)), err)
	}
	ps, _ := proc.List()
	total := self
	byExe := map[string]int64{}
	for _, p := range proc.Descendants(ps, pid) {
		if v, err := proc.PssKB(p.PID); err == nil {
			total += v
			byExe[childName(p)] += v
		}
	}
	parts := []string{"демон " + kbString(self)}
	names := make([]string, 0, len(byExe))
	for n := range byExe {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		parts = append(parts, n+" "+kbString(byExe[n]))
	}
	return fmt.Sprintf("%s Pss (%s; куча Go %s)", kbString(total), strings.Join(parts, " + "), kbString(int64(heapKB)))
}

func childName(p proc.Process) string {
	if p.HasArg("--gst-launch") {
		return "декодер"
	}
	return p.Exe()
}

func kbString(v int64) string {
	if v >= 1024 {
		return fmt.Sprintf("%.1f МБ", float64(v)/1024)
	}
	return fmt.Sprintf("%d КБ", v)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d дн %d ч", int(d.Hours())/24, int(d.Hours())%24)
}

func printJSON(c *Ctx, title string, v any) {
	if v == nil {
		return
	}
	b, _ := json.MarshalIndent(v, "  ", "  ")
	c.Printf("%s:\n  %s\n", title, b)
}
