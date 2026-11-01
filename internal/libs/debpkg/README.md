# debpkg — файлы из пакета .deb

Достаёт обычные файлы из пакета Debian без `dpkg`: архив `ar`, внутри `data.tar.xz` (также `.gz`, `.bz2`, `.tar`).
На колонке нет ни `dpkg`, ни `xz`, а декодера xz нет в стандартной библиотеке Go, поэтому используется `ulikunitz/xz` (`vendor/`, BSD-3-Clause).

```go
files, err := debpkg.Extract(r, func(name string) bool { return strings.HasSuffix(name, "/libc.so.6") })
```

Имена — как в пакете без ведущих `./` (`usr/lib/arm-linux-gnueabihf/libc.so.6`). Ссылки не возвращаются. Файлы больше 64 МБ отвергаются.
После последнего файла поток дочитывается до конца: так xz сверяет контрольную сумму, а оборванный пакет не сойдёт за целый.

Кому нужна: `spotify-soloist` (glibc 2.41 для Soloist). Тест — `deb_test.go` (пакеты собираются в памяти).
