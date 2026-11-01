package debpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func tarOf(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	for name, target := range links {
		if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	return b.Bytes()
}

func compress(t *testing.T, kind string, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	switch kind {
	case "xz":
		w, err := xz.NewWriter(&b)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(raw)
		w.Close()
	case "gz":
		w := gzip.NewWriter(&b)
		w.Write(raw)
		w.Close()
	case "":
		b.Write(raw)
	}
	return b.Bytes()
}

// debOf собирает пакет: debian-binary, control.tar.gz (нечётного размера, чтобы проверить выравнивание ar) и data.tar.<kind>.
func debOf(t *testing.T, kind string, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	member := func(name string, body []byte) {
		fmt.Fprintf(&b, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", name+"/", "0", "0", "0", "100644", len(body))
		b.Write(body)
		if len(body)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	member("debian-binary", []byte("2.0\n"))
	ctl := compress(t, "gz", tarOf(t, map[string]string{"control": "Package: x\n"}, nil))
	if len(ctl)%2 == 0 {
		ctl = append(ctl, 0) // хвост после gzip-потока допустим; нечётный размер проверяет выравнивание ar
	}
	member("control.tar.gz", ctl)
	suffix := ""
	if kind != "" {
		suffix = "." + kind
	}
	member("data.tar"+suffix, compress(t, kind, data))
	return b.Bytes()
}

func TestExtract(t *testing.T) {
	data := tarOf(t,
		map[string]string{
			"./usr/lib/arm-linux-gnueabihf/libc.so.6":           "LIBC",
			"./usr/lib/arm-linux-gnueabihf/ld-linux-armhf.so.3": "LD",
			"./usr/share/doc/libc6/copyright":                   "text",
		},
		map[string]string{"./lib/ld-linux-armhf.so.3": "/usr/lib/arm-linux-gnueabihf/ld-linux-armhf.so.3"})
	for _, kind := range []string{"xz", "gz", ""} {
		t.Run("data.tar."+kind, func(t *testing.T) {
			deb := debOf(t, kind, data)
			files, err := Extract(bytes.NewReader(deb), func(n string) bool { return strings.Contains(n, "/lib/") })
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, f := range files {
				got[f.Name] = string(f.Data)
				if f.Mode != 0o755 {
					t.Errorf("%s: права %v", f.Name, f.Mode)
				}
			}
			if len(got) != 2 || got["usr/lib/arm-linux-gnueabihf/libc.so.6"] != "LIBC" || got["usr/lib/arm-linux-gnueabihf/ld-linux-armhf.so.3"] != "LD" {
				t.Fatalf("получено %v (ссылка и копирайт возвращаться не должны)", got)
			}
		})
	}
}

func TestExtractErrors(t *testing.T) {
	if _, err := Extract(strings.NewReader("не ar-архив"), func(string) bool { return true }); err == nil {
		t.Error("не-ar принят")
	}
	if _, err := Extract(bytes.NewReader([]byte("!<arch>\n")), func(string) bool { return true }); err == nil {
		t.Error("пакет без data.tar принят")
	}
	deb := debOf(t, "xz", tarOf(t, map[string]string{"a": "b"}, nil))
	if _, err := Extract(bytes.NewReader(deb[:len(deb)-20]), func(string) bool { return true }); err == nil {
		t.Error("оборванный пакет принят")
	}
	zst := bytes.Replace(debOf(t, "xz", tarOf(t, map[string]string{"a": "b"}, nil)), []byte("data.tar.xz"), []byte("data.tar.zs"), 1)
	if _, err := Extract(bytes.NewReader(zst), func(string) bool { return true }); err == nil || !strings.Contains(err.Error(), "не поддержано") {
		t.Errorf("неизвестное сжатие: %v", err)
	}
}
