package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// verifyVendor сверяет файлы vendor/ со списком vendor.sha256 и запускает go mod verify. Список понимает оба формата
// sha256sum: «хэш  путь» (Linux) и «хэш *путь» (Git Bash на Windows).
func verifyVendor() error {
	want, err := readSums("vendor.sha256")
	if err != nil {
		return err
	}
	got := map[string]string{}
	err = filepath.WalkDir("vendor", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		sum, err := fileSHA256(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("vendor", p)
		got["./"+filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return err
	}
	var bad []string
	for p, s := range want {
		switch g, ok := got[p]; {
		case !ok:
			bad = append(bad, "нет файла "+p)
		case g != s:
			bad = append(bad, "изменён "+p)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			bad = append(bad, "лишний файл "+p)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("vendor/ отличается от vendor.sha256:\n  %s", strings.Join(bad, "\n  "))
	}
	fmt.Printf("vendor/: %d файлов совпадают с vendor.sha256\n", len(got))
	cmd := exec.Command("go", "mod", "verify")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func readSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		sum, p, ok := strings.Cut(line, " ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("%s: непонятная строка %q", path, line)
		}
		p = strings.TrimPrefix(strings.TrimPrefix(p, " "), "*")
		out[p] = sum
	}
	if len(out) == 0 {
		return nil, errors.New(path + ": пустой список")
	}
	return out, sc.Err()
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
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
