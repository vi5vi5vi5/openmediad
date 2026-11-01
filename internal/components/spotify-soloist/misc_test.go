package spotifysoloist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------- шим ----------

// Soloist берёт у libpulse ровно эти функции (список снят со строк его бинарника, docs/MUSIC-PLAN.md §10.2).
var shimSymbols = strings.Fields(`
pa_context_connect pa_context_disconnect pa_context_errno pa_context_get_sink_input_info pa_context_get_state pa_context_new
pa_context_set_sink_input_volume pa_context_set_state_callback pa_context_set_subscribe_callback pa_context_subscribe pa_context_unref
pa_cvolume_avg pa_cvolume_set pa_gettimeofday pa_operation_get_state pa_operation_unref pa_proplist_free pa_proplist_new pa_proplist_sets
pa_stream_connect_playback pa_stream_cork pa_stream_disconnect pa_stream_flush pa_stream_get_index pa_stream_get_state
pa_stream_get_timing_info pa_stream_is_corked pa_stream_new pa_stream_new_with_proplist pa_stream_set_started_callback
pa_stream_set_state_callback pa_stream_set_underflow_callback pa_stream_unref pa_stream_update_timing_info pa_stream_writable_size
pa_stream_write pa_strerror pa_threaded_mainloop_free pa_threaded_mainloop_get_api pa_threaded_mainloop_lock pa_threaded_mainloop_new
pa_threaded_mainloop_signal pa_threaded_mainloop_start pa_threaded_mainloop_stop pa_threaded_mainloop_unlock pa_threaded_mainloop_wait
pa_timeval_diff pa_usec_to_bytes`)

func TestShimExportsEverythingSoloistNeeds(t *testing.T) {
	if len(shimSymbols) != 48 {
		t.Fatalf("в списке %d символов, а Soloist берёт 48", len(shimSymbols))
	}
	f, err := elf.NewFile(bytes.NewReader(shimBlob))
	if err != nil {
		t.Fatal(err)
	}
	if f.Machine != elf.EM_ARM || f.Class != elf.ELFCLASS32 || f.Type != elf.ET_DYN {
		t.Fatalf("шим: %v %v %v — нужна разделяемая библиотека 32-битного ARM", f.Machine, f.Class, f.Type)
	}
	if sn, _ := f.DynString(elf.DT_SONAME); len(sn) != 1 || sn[0] != "libpulse.so.0" {
		t.Fatalf("soname шима %v, нужно libpulse.so.0", sn)
	}
	syms, err := f.DynamicSymbols()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, s := range syms {
		if s.Section != elf.SHN_UNDEF {
			have[s.Name] = true
		}
	}
	for _, name := range shimSymbols {
		if !have[name] {
			t.Errorf("шим не экспортирует %s", name)
		}
	}
	// шим грузится системной glibc колонки (2.30): версии символов выше не годятся
	imp, _ := f.ImportedSymbols()
	for _, s := range imp {
		if !strings.HasPrefix(s.Version, "GLIBC_") {
			continue
		}
		if v := strings.Split(strings.TrimPrefix(s.Version, "GLIBC_"), "."); len(v) >= 2 {
			maj, _ := strconv.Atoi(v[0])
			min, _ := strconv.Atoi(v[1])
			if maj > 2 || maj == 2 && min > 30 {
				t.Errorf("шим требует %s для %s: на колонке glibc 2.30", s.Version, s.Name)
			}
		}
	}
}

func TestShimHashMatches(t *testing.T) {
	if !shimHashOK() {
		t.Fatal("assets/libpulse.so.0 не совпадает с assets/libpulse.so.0.sha256: пересоберите шим (shim/build.sh)")
	}
}

// ---------- кэш ----------

func TestCacheGuardTrimsOldestWhenSpaceIsLow(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 10; i++ { // по 1 МБ, i=0 — самый старый
		p := filepath.Join(dir, fmt.Sprintf("%02d", i), fmt.Sprintf("%02d.file", i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, make([]byte, 1<<20), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour))
	}
	os.WriteFile(filepath.Join(dir, "index.dat"), []byte("индекс"), 0o644) // не *.file: не трогать
	freeMB := int64(10)                                                    // меньше keep_free: чистить
	g := &cacheGuard{dir: dir, maxMB: 100, keepFreeMB: 20, logf: func(string, ...any) {},
		freeKB: func(string) (int64, error) { return freeMB * 1024, nil }}
	n := g.check()
	if n < 10 || n > 11 { // нужно поднять свободное с 10 до 30 МБ: 20 МБ, но файлов всего 10
		t.Logf("удалено %d файлов", n)
	}
	left := 0
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%02d", i), fmt.Sprintf("%02d.file", i))); err == nil {
			left++
		}
	}
	if left != 0 {
		t.Fatalf("осталось %d файлов: нужно было удалить всё ради 30 МБ свободных", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.dat")); err != nil {
		t.Fatal("индекс (не *.file) удалён")
	}
	if st := g.status(); st.Trims != 1 {
		t.Fatalf("статус %+v", st)
	}
}

func TestCacheGuardKeepsOldestFirstOrder(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 5; i++ { // по 6 МБ, 0.file — самый старый
		p := filepath.Join(dir, fmt.Sprintf("%d.file", i))
		os.WriteFile(p, make([]byte, 6<<20), 0o644)
		os.Chtimes(p, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour))
	}
	freeMB := int64(19) // меньше keep_free (20): чистить, пока не станет keep_free+10 = 30 МБ
	g := &cacheGuard{dir: dir, maxMB: 100, keepFreeMB: 20, logf: func(string, ...any) {},
		freeKB: func(string) (int64, error) { return freeMB * 1024, nil }}
	if n := g.check(); n != 2 { // 19 → 25 → 31 МБ: хватило двух файлов
		t.Fatalf("удалено %d файлов, ждали 2", n)
	}
	for i, wantGone := range []bool{true, true, false, false, false} {
		_, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%d.file", i)))
		if gone := err != nil; gone != wantGone {
			t.Errorf("%d.file: удалён=%v, ждали %v (первыми уходят самые старые)", i, gone, wantGone)
		}
	}
}

func TestCacheGuardLeavesHealthyCacheAlone(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.file"), make([]byte, 5<<20), 0o644)
	g := &cacheGuard{dir: dir, maxMB: 100, keepFreeMB: 20, logf: func(string, ...any) {},
		freeKB: func(string) (int64, error) { return 100 * 1024, nil }}
	if n := g.check(); n != 0 {
		t.Fatalf("при достатке места удалено %d файлов", n)
	}
	if st := g.status(); st.SizeMB != 5 || st.FreeMB != 100 {
		t.Fatalf("статус %+v", st)
	}
}

// ---------- cp15_barrier ----------

func TestFixCP15(t *testing.T) {
	root := t.TempDir()
	if _, changed, err := fixCP15(root); err != nil || changed {
		t.Fatalf("без параметра ядра: changed=%v err=%v (не колонка — не ошибка)", changed, err)
	}
	p := filepath.Join(root, cp15Rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("1\n"), 0o644)
	was, changed, err := fixCP15(root)
	if err != nil || !changed || was != "1" {
		t.Fatalf("was=%q changed=%v err=%v", was, changed, err)
	}
	if cp15Value(root) != "2" {
		t.Fatalf("значение %q, ждали 2", cp15Value(root))
	}
	if _, changed, _ := fixCP15(root); changed {
		t.Fatal("повторный вызов снова меняет")
	}
	if cp15Value(t.TempDir()) != "?" {
		t.Fatal("нет параметра — должен быть «?»")
	}
}

// ---------- раскладка ----------

func TestMissingAndExpiry(t *testing.T) {
	p := newPaths(t.TempDir(), t.TempDir(), "")
	if m := p.missing(); len(m) != 5 {
		t.Fatalf("в пустом каталоге не хватает %v, ждали 5 частей", m)
	}
	for _, f := range []string{p.soloist(), p.loader(), filepath.Join(p.lib(), "libc.so.6"), p.shim(), p.key()} {
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte("x"), 0o644)
	}
	if m := p.missing(); len(m) != 0 {
		t.Fatalf("всё на месте, а не хватает %v", m)
	}
	custom := newPaths(p.dir, p.run, filepath.Join(t.TempDir(), "k"))
	if m := custom.missing(); len(m) != 1 || m[0] != "ключ Soloist" {
		t.Fatalf("свой файл ключа: не хватает %v", m)
	}

	var m installMeta
	m.Soloist.BuildDate = "20261030"
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if got := expiryText(m, now); !strings.Contains(got, "ещё 87 дн") {
		t.Fatalf("срок: %q (сборка 30.10 + 90 дней, на 1.11 осталось 87)", got)
	}
	if got := expiryText(m, now.AddDate(0, 4, 0)); !strings.Contains(got, "истекла") {
		t.Fatalf("срок: %q", got)
	}
	if got := expiryText(installMeta{}, now); !strings.Contains(got, "неизвестен") {
		t.Fatalf("срок без даты: %q", got)
	}
}

// ---------- установка ----------

// fakeDeb собирает пакет Debian с нужными библиотеками glibc (data.tar.gz) и возвращает его с хэшем.
func fakeDeb(t *testing.T, files []string) ([]byte, string) {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, f := range files {
		body := []byte("библиотека " + f)
		tw.WriteHeader(&tar.Header{Name: "./usr/lib/arm-linux-gnueabihf/" + f, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write(body)
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(tb.Bytes())
	zw.Close()
	var deb bytes.Buffer
	deb.WriteString("!<arch>\n")
	fmt.Fprintf(&deb, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", "data.tar.gz/", "0", "0", "0", "100644", gz.Len())
	deb.Write(gz.Bytes())
	if gz.Len()%2 == 1 {
		deb.WriteByte('\n')
	}
	sum := sha256.Sum256(deb.Bytes())
	return deb.Bytes(), hex.EncodeToString(sum[:])
}

func fakeSoloistArchive(t *testing.T, elfMachine byte) []byte {
	t.Helper()
	bin := make([]byte, 64)
	copy(bin, "\x7fELF")
	bin[4], bin[5], bin[18] = 1, 1, elfMachine
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for name, body := range map[string][]byte{"soloist_release_arm32/CHANGELOG.md": []byte("x"), "soloist_release_arm32/soloist": bin} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write(body)
	}
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(tb.Bytes())
	zw.Close()
	return gz.Bytes()
}

func installRig(t *testing.T, elfMachine byte) (*installer, paths) {
	t.Helper()
	dir := t.TempDir()
	from := filepath.Join(dir, "from")
	os.MkdirAll(from, 0o755)
	deb, sum := fakeDeb(t, glibcFiles)
	os.WriteFile(filepath.Join(from, glibcDeb), deb, 0o644)
	os.WriteFile(filepath.Join(from, "soloist_release_arm32.tar.gz"), fakeSoloistArchive(t, elfMachine), 0o644)
	old := glibcSHA
	glibcSHA = sum
	t.Cleanup(func() { glibcSHA = old })
	p := newPaths(filepath.Join(dir, "inst"), t.TempDir(), "")
	in := newInstaller(p, func(string, ...any) {})
	in.fromDir = from
	in.smoke = func(paths) (string, error) {
		return "soloist 1.3.10.31 build 1793361706 (20261030) (g19412df6ea) (linux/arm)\nclient expires in 88 days\n", nil
	}
	return in, p
}

func TestInstallOffline(t *testing.T) {
	in, p := installRig(t, 40)
	if err := in.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	for _, f := range glibcFiles {
		if !fileExists(filepath.Join(p.lib(), f)) {
			t.Errorf("нет %s", f)
		}
	}
	if !fileExists(p.soloist()) || !fileExists(p.shim()) {
		t.Fatal("не записаны Soloist или шим")
	}
	b, _ := os.ReadFile(p.shim())
	if !bytes.Equal(b, shimBlob) {
		t.Fatal("записанный шим не совпадает с вшитым")
	}
	m, err := readMeta(p.meta())
	if err != nil || m.Soloist.Version != "1.3.10.31" || m.Soloist.BuildDate != "20261030" || m.Glibc.Package != glibcDeb || m.ShimSHA256 != shimSHA256() {
		t.Fatalf("install.json: %+v, %v", m, err)
	}
	if got := p.missing(); len(got) != 1 || got[0] != "ключ Soloist" {
		t.Fatalf("после установки не хватает %v, ждали только ключ", got)
	}
	// повторная установка ничего не ломает и не качает заново
	if err := in.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRejectsWrongGlibcHash(t *testing.T) {
	in, _ := installRig(t, 40)
	glibcSHA = strings.Repeat("0", 64)
	err := in.Install(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("подменённый пакет принят: %v", err)
	}
}

func TestInstallRejectsWrongArchitecture(t *testing.T) {
	in, p := installRig(t, 183) // EM_AARCH64
	err := in.Install(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "ARM") {
		t.Fatalf("сборка не под ARM принята: %v", err)
	}
	if fileExists(p.soloist()) {
		t.Fatal("негодный файл остался на диске")
	}
}

func TestInstallFromEmptyDirFails(t *testing.T) {
	in, _ := installRig(t, 40)
	in.fromDir = t.TempDir()
	if err := in.Install(context.Background(), false); err == nil {
		t.Fatal("установка без файлов прошла")
	}
}

func TestInstallSmokeFailureIsReported(t *testing.T) {
	in, _ := installRig(t, 40)
	in.smoke = func(paths) (string, error) { return "", fmt.Errorf("GLIBC_2.38 not found") }
	if err := in.Install(context.Background(), false); err == nil || !strings.Contains(err.Error(), "не запускается") {
		t.Fatalf("неработающий Soloist принят: %v", err)
	}
}

func TestSetKey(t *testing.T) {
	p := newPaths(t.TempDir(), t.TempDir(), "")
	if err := p.setKey(strings.NewReader("  spak_abcdefghijklmnopqrstuvwxyz0123456 \r\n")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.key())
	if string(b) != "spak_abcdefghijklmnopqrstuvwxyz0123456\n" {
		t.Fatalf("записано %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p.key()); st.Mode().Perm() != 0o600 {
			t.Fatalf("права ключа %v, нужно 0600", st.Mode().Perm())
		}
	}
	for _, bad := range []string{"", "короткий", "два слова в ключе подряд двадцать символов", "ключ_с_кириллицей_достаточной_длины_1234"} {
		if err := p.setKey(strings.NewReader(bad)); err == nil {
			t.Errorf("ключ %q принят", bad)
		}
	}
}

// ---------- настройки ----------

func TestConfigValidation(t *testing.T) {
	if err := defaults().validate(); err != nil {
		t.Fatalf("умолчания не проходят проверку: %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"пустое имя":      func(c *Config) { c.DeviceName = " " },
		"имя с переводом": func(c *Config) { c.DeviceName = "a\nb" },
		"громкость 101":   func(c *Config) { c.Volume = 101 },
		"кэш меньше 100":  func(c *Config) { c.CacheMB = 50 },
		"порт 80":         func(c *Config) { c.WSPort = 80 },
		"места в обрез":   func(c *Config) { c.KeepFreeMB = 1 },
		"задержка 10 с":   func(c *Config) { c.LatencyMS = 10000 },
	} {
		c := defaults()
		mut(c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
}
