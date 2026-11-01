package spotifysoloist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"openmediad/internal/libs/debpkg"
)

// Что скачивается при установке.
//
//   - Soloist — закрытая программа Spotify. Её нельзя распространять, поэтому проект её не содержит: пользователь качает её сам
//     с CDN Spotify. Сборки обновляются, хэш зафиксировать нельзя: проверяются формат (ELF32 ARM) и то, что она запускается.
//   - glibc — свободная (LGPL) библиотека из Debian. Системная glibc колонки 2.30, а Soloist требует не ниже 2.38, поэтому рядом
//     кладётся своя, и Soloist запускается через её загрузчик. Хэш пакета зафиксирован.
const (
	soloistURL = "https://soloist-builds.spotifycdn.com/soloist_release_arm32.tar.gz"
	glibcDeb   = "libc6_2.41-12+deb13u4_armhf.deb"

	maxSoloistArchive = 80 << 20
	maxSoloistBinary  = 120 << 20
)

// glibcSHA — зафиксированный хэш пакета (переменная, чтобы тесты могли подставить свой).
var glibcSHA = "4fc6fed8d77d01c0dacb5fda8b880a8c8a669575936de5761b693077a6f6c2ec"

// glibcURLs — откуда брать пакет: зеркало и неизменяемый снимок архива (на случай, если пакет уже заменили новой версией).
var glibcURLs = []string{
	"https://deb.debian.org/debian/pool/main/g/glibc/" + glibcDeb,
	"https://snapshot.debian.org/archive/debian/20261030T000000Z/pool/main/g/glibc/" + glibcDeb,
}

// glibcFiles — какие библиотеки берём из пакета (в каталог lib/ рядом с Soloist).
var glibcFiles = []string{
	"ld-linux-armhf.so.3", "libc.so.6", "libm.so.6", "libpthread.so.0", "libdl.so.2", "librt.so.1", "libresolv.so.2",
	"libnss_dns.so.2", "libnss_files.so.2",
}

// installer ставит и обновляет Soloist с glibc и шимом.
type installer struct {
	p    paths
	say  func(string, ...any) // сообщения пользователю
	http *http.Client
	// FromDir — каталог с уже скачанными файлами (soloist_release_arm32.tar.gz и пакет libc6 .deb): установка без сети.
	fromDir string
	// smoke запускает установленный Soloist с -V и возвращает его вывод; в тестах подменяется. nil — запуск на этом компьютере
	// невозможен (проверка пропускается).
	smoke func(p paths) (string, error)
}

func newInstaller(p paths, say func(string, ...any)) *installer {
	in := &installer{p: p, say: say, http: &http.Client{Timeout: 10 * time.Minute}}
	if runtime.GOOS == "linux" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "arm") {
		in.smoke = smokeRun
	}
	return in
}

// Install ставит всё недостающее: glibc, шим, Soloist. force переустанавливает и то, что уже есть.
func (in *installer) Install(ctx context.Context, force bool) error {
	for _, d := range []string{in.p.dir, in.p.lib(), in.p.data(), in.p.cache()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	meta, _ := readMeta(in.p.meta())
	if err := in.installGlibc(ctx, &meta, force); err != nil {
		return err
	}
	if err := in.installShim(&meta); err != nil {
		return err
	}
	if force || !fileExists(in.p.soloist()) {
		if err := in.installSoloist(ctx, &meta); err != nil {
			return err
		}
	} else {
		in.say("Soloist уже установлен (%s)", orDash(meta.Soloist.Version))
	}
	return writeMeta(in.p.meta(), meta)
}

// Update скачивает свежую сборку Soloist поверх установленной (ключ и сеанс остаются).
func (in *installer) Update(ctx context.Context) error {
	meta, _ := readMeta(in.p.meta())
	if err := in.installSoloist(ctx, &meta); err != nil {
		return err
	}
	return writeMeta(in.p.meta(), meta)
}

func (in *installer) installGlibc(ctx context.Context, meta *installMeta, force bool) error {
	have := true
	for _, f := range glibcFiles {
		if !fileExists(filepath.Join(in.p.lib(), f)) {
			have = false
		}
	}
	if have && !force && meta.Glibc.SHA256 == glibcSHA {
		in.say("glibc уже установлена (%s)", glibcDeb)
		return nil
	}
	in.say("glibc: %s", glibcDeb)
	deb, err := in.load(ctx, glibcDeb, glibcURLs, 8<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(deb)
	if hex.EncodeToString(sum[:]) != glibcSHA {
		return fmt.Errorf("пакет %s: sha256 %x не совпал с зафиксированным %s — файл подменён или это другая версия", glibcDeb, sum, glibcSHA)
	}
	want := map[string]bool{}
	for _, f := range glibcFiles {
		want[f] = true
	}
	files, err := debpkg.Extract(bytes.NewReader(deb), func(name string) bool {
		return want[path.Base(name)] && strings.Contains(name, "lib/")
	})
	if err != nil {
		return err
	}
	got := map[string]bool{}
	for _, f := range files {
		base := path.Base(f.Name)
		if got[base] {
			continue
		}
		got[base] = true
		if err := writeFileAtomic(filepath.Join(in.p.lib(), base), f.Data, 0o755); err != nil {
			return err
		}
	}
	for _, f := range glibcFiles {
		if !got[f] {
			return fmt.Errorf("в пакете %s нет %s", glibcDeb, f)
		}
	}
	meta.Glibc.Package, meta.Glibc.SHA256 = glibcDeb, glibcSHA
	in.say("  записано библиотек: %d (%s)", len(got), in.p.lib())
	return nil
}

func (in *installer) installShim(meta *installMeta) error {
	if !shimHashOK() {
		return errors.New("вшитый шим libpulse не совпадает со своим хэшем: пересоберите (shim/build.sh) или не правьте assets/")
	}
	if err := writeFileAtomic(in.p.shim(), shimBlob, 0o755); err != nil {
		return err
	}
	meta.ShimSHA256 = shimSHA256()
	in.say("шим libpulse записан (%d КБ)", len(shimBlob)>>10)
	return nil
}

var versionRE = regexp.MustCompile(`soloist (\S+) build \d+ \((\d{8})\)`)

func (in *installer) installSoloist(ctx context.Context, meta *installMeta) error {
	in.say("Soloist: скачиваю с CDN Spotify (закрытая программа Spotify, остаётся только на этой колонке)")
	// Всё идёт потоком через файлы: на колонке свободно около 90 МБ ОЗУ, а бинарник Soloist 29 МБ, и целиком в память он не лезет
	// (первый запуск установки на колонке был убит по памяти).
	arc, cleanup, err := in.archive(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	sum, err := fileSHA256(arc)
	if err != nil {
		return err
	}
	if err := extractSoloist(arc, in.p.soloist()); err != nil {
		return err
	}
	meta.Soloist.SHA256 = sum
	meta.Soloist.InstalledAt = time.Now().UTC()
	if in.smoke != nil {
		out, err := in.smoke(in.p)
		if err != nil {
			return fmt.Errorf("Soloist установлен, но не запускается (-V): %w", err)
		}
		if m := versionRE.FindStringSubmatch(out); m != nil {
			meta.Soloist.Version, meta.Soloist.BuildDate = m[1], m[2]
		}
		in.say("  запускается: %s", strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
	} else {
		in.say("  запуск не проверен: это не колонка")
	}
	in.say("  %s", expiryText(*meta, time.Now()))
	return nil
}

// archive возвращает путь к архиву Soloist: файл из -from или скачанный во временный файл (cleanup его удалит).
func (in *installer) archive(ctx context.Context) (path string, cleanup func(), err error) {
	const name = "soloist_release_arm32.tar.gz"
	if in.fromDir != "" {
		p := filepath.Join(in.fromDir, name)
		if !fileExists(p) {
			return "", nil, fmt.Errorf("в %s нет файла %s", in.fromDir, name)
		}
		in.say("  взят из %s", in.fromDir)
		return p, func() {}, nil
	}
	tmp := filepath.Join(in.p.dir, name+".download")
	if err := in.downloadToFile(ctx, soloistURL, maxSoloistArchive, tmp); err != nil {
		os.Remove(tmp)
		return "", nil, fmt.Errorf("не удалось скачать %s (можно положить файл рядом и указать -from КАТАЛОГ): %w", name, err)
	}
	return tmp, func() { os.Remove(tmp) }, nil
}

// downloadToFile качает url в файл, не держа его в памяти.
func (in *installer) downloadToFile(ctx context.Context, url string, limit int64, dst string) error {
	do := func(c *http.Client) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("ответ %s", resp.Status)
		}
		if resp.ContentLength > limit {
			return fmt.Errorf("файл больше ожидаемого (%d байт)", resp.ContentLength)
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if n > limit {
			return fmt.Errorf("файл больше ожидаемого (> %d байт)", limit)
		}
		in.say("  получено %d КБ", n>>10)
		return nil
	}
	err := do(in.http)
	var unk x509.UnknownAuthorityError
	if err != nil && errors.As(err, &unk) {
		if c := fallbackClient(); c != nil {
			return do(c)
		}
	}
	return err
}

// extractSoloist достаёт бинарник soloist из tar.gz Spotify прямо в файл dst (через временное имя), проверяя, что это ELF
// для 32-битного ARM.
func extractSoloist(arc, dst string) error {
	f, err := os.Open(arc)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("архив Soloist: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("в архиве Soloist нет файла soloist")
		}
		if err != nil {
			return fmt.Errorf("архив Soloist: %w", err)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != "soloist" {
			continue
		}
		if h.Size < 64 || h.Size > maxSoloistBinary {
			return fmt.Errorf("soloist в архиве странного размера: %d байт", h.Size)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".new"
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		fail := func(err error) error {
			out.Close()
			os.Remove(tmp)
			return err
		}
		head := make([]byte, 64)
		if _, err := io.ReadFull(tr, head); err != nil {
			return fail(fmt.Errorf("архив Soloist: %w", err))
		}
		if err := checkELFARM32(head); err != nil {
			return fail(err)
		}
		if _, err := out.Write(head); err != nil {
			return fail(err)
		}
		if _, err := io.Copy(out, io.LimitReader(tr, h.Size-int64(len(head)))); err != nil {
			return fail(err)
		}
		if err := out.Close(); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
}

// fileSHA256 — sha256 файла (потоком).
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkELFARM32 — это ELF для 32-битного ARM (а не архив ошибки или сборка под другую архитектуру).
func checkELFARM32(b []byte) error {
	if len(b) < 20 || string(b[:4]) != "\x7fELF" {
		return errors.New("soloist: это не ELF")
	}
	if b[4] != 1 || b[5] != 1 || b[18] != 40 || b[19] != 0 { // 32 бита, little-endian, EM_ARM = 40
		return errors.New("soloist: нужна сборка для 32-битного ARM (soloist_release_arm32)")
	}
	return nil
}

// load берёт файл из fromDir (если задан и файл там есть) или скачивает по первому рабочему адресу.
func (in *installer) load(ctx context.Context, name string, urls []string, limit int64) ([]byte, error) {
	if in.fromDir != "" {
		if b, err := os.ReadFile(filepath.Join(in.fromDir, name)); err == nil {
			in.say("  взят из %s", in.fromDir)
			return b, nil
		}
		return nil, fmt.Errorf("в %s нет файла %s", in.fromDir, name)
	}
	var last error
	for _, u := range urls {
		b, err := in.download(ctx, u, limit)
		if err == nil {
			return b, nil
		}
		last = err
		in.say("  %s: %v", hostOf(u), err)
	}
	return nil, fmt.Errorf("не удалось скачать %s (можно положить файл рядом и указать -from КАТАЛОГ): %w", name, last)
}

func hostOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}

func (in *installer) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	b, err := in.get(ctx, in.http, url, limit)
	var unk x509.UnknownAuthorityError
	if err != nil && errors.As(err, &unk) {
		// корневых сертификатов системы не хватило: запасной набор прошивки (его же использует yandex-music)
		if c := fallbackClient(); c != nil {
			return in.get(ctx, c, url, limit)
		}
	}
	return b, err
}

func (in *installer) get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ответ %s", resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("файл больше ожидаемого (%d байт)", resp.ContentLength)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("файл больше ожидаемого (> %d байт)", limit)
	}
	in.say("  получено %d КБ", len(b)>>10)
	return b, nil
}

// fallbackClient — клиент с корневыми сертификатами прошивки колонки; nil, если файла нет.
func fallbackClient() *http.Client {
	pem, err := os.ReadFile("/system/vendor/quasar/ca-certificates.crt")
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig.RootCAs = pool
	return &http.Client{Timeout: 10 * time.Minute, Transport: tr}
}

// smokeRun запускает установленный Soloist с -V через загрузчик glibc (на колонке).
func smokeRun(p paths) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.loader(), "--library-path", p.lib()+":/lib", p.soloist(), "-V").CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// keyRE — ключ Soloist: печатные символы без пробелов, разумной длины. Формат сервер не раскрывает, поэтому строже не проверяем.
var keyRE = regexp.MustCompile(`^[\x21-\x7e]{16,256}$`)

// SetKey читает ключ (одна строка) и записывает его в файл ключа с правами 0600.
func (p paths) setKey(r io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(string(b))
	if !keyRE.MatchString(key) {
		return errors.New("это не похоже на ключ Soloist: нужна одна строка без пробелов (скопируйте ключ из Developer Dashboard Spotify целиком)")
	}
	return writeFileAtomic(p.key(), []byte(key+"\n"), 0o600)
}
