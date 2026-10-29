// Package layout — где на колонке лежат файлы openmediad (PLAN.md, §8.1).
//
//	/data/opt/openmediad/          всё своё на постоянном разделе
//	  bin/openmediad               бинарник
//	  guard.sh                     страж (пишется из шаблона в бинарнике)
//	  openmediad.conf              настройки
//	  backup/                      оригиналы изменённых файлов rootfs и manifest.json
//	  state/                       «автозапуск выключен» и прочие отметки, переживающие перезагрузку
//	/etc/init.d/S98openmediad      скрипт автозапуска — единственный наш файл в rootfs (режим shim)
//	/tmp/openmediad/               ctl.sock, pid, журнал стража (tmpfs — это ОЗУ: только маленькие файлы)
package layout

import (
	"os"
	"path/filepath"

	"openmediad/internal/core/ctlsock"
)

// Root — префикс всех путей: пусто на колонке; временный каталог в тестах и в симуляторе колонки на компьютере
// (переменная OPENMEDIAD_ROOT, scripts/dev/simulate.py).
var Root = os.Getenv("OPENMEDIAD_ROOT")

// Пути на колонке без префикса.
const (
	HomeDir    = "/data/opt/openmediad"
	InitScript = "/etc/init.d/S98openmediad"
)

func Home() string           { return Root + HomeDir }
func Bin() string            { return filepath.Join(Home(), "bin", "openmediad") }
func InstalledGuard() string { return filepath.Join(Home(), "guard.sh") }
func Backup() string         { return filepath.Join(Home(), "backup") }
func State() string          { return filepath.Join(Home(), "state") }
func Init() string           { return Root + InitScript }

// Run — каталог рабочих файлов (сокет, pid, журнал стража). Другой — OPENMEDIAD_RUN.
func Run() string { return ctlsock.RunDir() }

// AutostartOff — отметка «автозапуск выключен» (openmediad disable).
func AutostartOff() string { return filepath.Join(State(), "autostart-off") }

// Installed — openmediad установлен на /data (бинарник и страж на месте).
func Installed() bool { return exists(Bin()) && exists(InstalledGuard()) }

// Guard — страж, которым пользуются start и stop: установленный, а без установки — во временном каталоге
// (так openmediad можно включить до перезагрузки, ничего не устанавливая).
func Guard() string {
	if Installed() {
		return InstalledGuard()
	}
	return filepath.Join(Run(), "guard.sh")
}

// Файлы стража в Run().
func GuardPID() string   { return filepath.Join(Run(), "guard.pid") }
func DaemonPID() string  { return filepath.Join(Run(), "daemon.pid") }
func GuardState() string { return filepath.Join(Run(), "guard.state") }
func GuardLog() string   { return filepath.Join(Run(), "guard.log") }
func DaemonErr() string  { return filepath.Join(Run(), "daemon.err") }
func Crashes() string    { return filepath.Join(Run(), "crashes") }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
