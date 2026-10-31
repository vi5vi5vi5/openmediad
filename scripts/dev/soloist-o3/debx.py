"""Достать файлы из .deb (armhf) без dpkg.

  debx.py пакет.deb каталог имя1 имя2 ...   библиотеки из usr/lib/arm-linux-gnueabihf и /lib с точным совпадением имени
  debx.py пакет.deb каталог --include       заголовки usr/include/pulse/*.h -> каталог/pulse/

Запускать как «python -I debx.py ...».
"""
import io
import os
import sys
import tarfile


def main():
    deb_path, out, names = sys.argv[1], sys.argv[2], set(sys.argv[3:])
    deb = open(deb_path, "rb").read()
    if deb[:8] != b"!<arch>\n":
        sys.exit("не ar-архив: " + deb_path)
    i, members = 8, {}
    while i < len(deb):
        name = deb[i:i + 16].decode().strip().rstrip("/")
        size = int(deb[i + 48:i + 58].decode().strip())
        members[name] = deb[i + 60:i + 60 + size]
        i += 60 + size + (size & 1)
    data = next((n for n in members if n.startswith("data.tar")), None)
    if data is None:
        sys.exit("в пакете нет data.tar*")
    mode = {".xz": "r:xz", ".gz": "r:gz", ".bz2": "r:bz2"}.get(os.path.splitext(data)[1])
    if mode is None:
        sys.exit("формат %s не поддержан (нужен xz/gz/bz2)" % data)
    tf = tarfile.open(fileobj=io.BytesIO(members[data]), mode=mode)
    os.makedirs(out, exist_ok=True)
    if names == {"--include"}:
        n = 0
        for m in tf.getmembers():
            if m.isfile() and "/usr/include/pulse/" in m.name:
                dst = os.path.join(out, "pulse", os.path.basename(m.name))
                os.makedirs(os.path.dirname(dst), exist_ok=True)
                with open(dst, "wb") as f:
                    f.write(tf.extractfile(m).read())
                n += 1
        if n == 0:
            sys.exit("в пакете нет заголовков pulse")
        print("заголовков:", n)
        return
    found = set()
    for m in tf.getmembers():
        base = os.path.basename(m.name)
        if m.isfile() and base in names and ("/lib/" in m.name or "/arm-linux-gnueabihf/" in m.name):
            with open(os.path.join(out, base), "wb") as f:
                f.write(tf.extractfile(m).read())
            os.chmod(os.path.join(out, base), 0o755)
            found.add(base)
    missing = names - found
    if missing:
        sys.exit("не нашли в пакете: " + ", ".join(sorted(missing)))
    print("извлечено:", ", ".join(sorted(found)))


main()
