#!/usr/bin/env python3
"""Симулятор колонки на компьютере: проверка start, stop, restart, install, uninstall, doctor и стража без колонки.

Что подделано:
  * корень колонки — временный каталог (OPENMEDIAD_ROOT): maind, aplay, /etc/init.d, /data;
  * штатный mediad — процесс «maind ... --mediad» на 127.0.0.1:9906 и лаунчер, который поднимает его после SIGTERM;
  * platformd — 4 соединения с плеером; правило iptables «есть» → идут на шим (19906), «нет» → на 9906 напрямую;
  * aliced — приветствие с моделью bergamot на 127.0.0.1:9875;
  * iptables — сценарий, который хранит правила в файле (настоящая сеть не трогается).

Настоящие: бинарник openmediad (демон, шим, страж guard.sh, установка с бэкапом), /proc.

Запуск (Linux, от root — команды установки проверяют права; порты 9906, 9875, 19906, 6701 должны быть свободны):
    go run ./tools/build -host -o dist/openmediad-host
    sudo python3 scripts/dev/simulate.py dist/openmediad-host
"""
import os
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time

FIRMWARE = "0.369.1.50.4794652521.20261021.117"

MEDIAD = r"""
import os, socket, signal, sys, threading, time
signal.signal(signal.SIGTERM, lambda *a: os._exit(0))
parent = os.getppid()
def orphan():  # симулятор завершился — выйти и не держать порт
    while os.getppid() == parent: time.sleep(0.5)
    os._exit(0)
threading.Thread(target=orphan, daemon=True).start()
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 9906)); s.listen(16)
def serve(c):
    while c.recv(65536): pass
while True:
    c, _ = s.accept()
    threading.Thread(target=serve, args=(c,), daemon=True).start()
"""

IPTABLES = """#!/bin/sh
R="$SIM/rules"; touch "$R"
shift 2; op=$1; shift 2
case $op in
-S) echo "-P OUTPUT ACCEPT"; cat "$R" ;;
-I) shift; echo "-A OUTPUT $*" >> "$R" ;;
-D) grep -qxF -- "-A OUTPUT $*" "$R" || exit 1
    awk -v r="-A OUTPUT $*" 'BEGIN{d=0} $0==r && !d {d=1; next} {print}' "$R" > "$R.new"; mv "$R.new" "$R" ;;
esac
"""


def pb_varint(n):
    out = b""
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out += bytes([b | 0x80])
        else:
            return out + bytes([b])


def pb_bytes(num, data):
    return pb_varint(num << 3 | 2) + pb_varint(len(data)) + data


def handshake():
    dev = pb_bytes(1, b"aliced") + pb_bytes(2, FIRMWARE.encode()) + pb_bytes(11, b"bergamot")
    body = pb_bytes(1, pb_bytes(1, dev))
    msg = pb_bytes(252, body) + pb_varint(253 << 3) + pb_varint(207)
    return struct.pack("<I", len(msg)) + msg


class Sim:
    def __init__(self, binary):
        self.binary = os.path.abspath(binary)
        self.dir = tempfile.mkdtemp(prefix="omd-sim-")
        self.root = os.path.join(self.dir, "root")
        self.stop = threading.Event()
        self.mediad = None
        self.failures = 0
        for d in ["system/vendor/quasar", "usr/bin", "etc/init.d", "data", "proc"]:
            os.makedirs(os.path.join(self.root, d), exist_ok=True)
        open(os.path.join(self.root, "system/vendor/quasar/maind"), "w").close()
        self.write("root/usr/bin/aplay", "#!/bin/sh\ncat > /dev/null\n", 0o755)
        os.makedirs(os.path.join(self.dir, "bin"))
        self.write("bin/iptables", IPTABLES, 0o755)
        self.env = dict(os.environ, SIM=self.dir, OPENMEDIAD_ROOT=self.root,
                        OPENMEDIAD_RUN=os.path.join(self.dir, "run"),
                        PATH=os.path.join(self.dir, "bin") + ":" + os.environ["PATH"])
        self.env.pop("OPENMEDIAD_CONFIG", None)

    def write(self, rel, text, mode=0o644):
        p = os.path.join(self.dir, rel)
        with open(p, "w") as f:
            f.write(text)
        os.chmod(p, mode)

    def rules(self):
        try:
            with open(os.path.join(self.dir, "rules")) as f:
                return [l for l in f.read().splitlines() if l.strip()]
        except FileNotFoundError:
            return []

    # --- поддельные процессы платформы ---

    def launcher(self):
        while not self.stop.is_set():
            self.mediad = subprocess.Popen(["maind", "-c", MEDIAD, "--mediad"], executable=sys.executable)
            self.mediad.wait()
            time.sleep(0.5)

    def platform_conn(self):
        while not self.stop.is_set():
            port = 19906 if any("19906" in r for r in self.rules()) else 9906
            try:
                c = socket.create_connection(("127.0.0.1", port), timeout=1)
            except OSError:
                time.sleep(0.5)
                continue
            c.settimeout(None)
            try:
                while c.recv(65536):
                    pass
            except OSError:
                pass
            c.close()
            time.sleep(0.3)

    def aliced(self):
        s = socket.socket()
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("127.0.0.1", 9875))
        s.listen(8)
        s.settimeout(0.5)
        while not self.stop.is_set():
            try:
                c, _ = s.accept()
            except socket.timeout:
                continue
            try:
                c.sendall(handshake())
            except OSError:
                pass
            threading.Timer(3, c.close).start()

    def conns(self):
        via, clients = 0, 0
        with open("/proc/net/tcp") as f:
            next(f)
            for line in f:
                p = line.split()
                if p[3] != "01":
                    continue
                if p[1].endswith(":4DC2"):
                    via += 1
                elif p[2].endswith(":26B2") and not p[1].startswith("0200007F:"):
                    clients += 1
        return via, max(clients - via, 0)

    # --- проверки ---

    def omd(self, *args, show=True):
        r = subprocess.run([self.binary, *args], env=self.env, capture_output=True, text=True, timeout=300)
        if show:
            out = (r.stdout + r.stderr).rstrip()
            print("$ openmediad " + " ".join(args) + f"   (код {r.returncode})")
            if out:
                print("  " + out.replace("\n", "\n  "))
        return r

    def check(self, what, ok):
        print(("  ✓ " if ok else "  ✗ ") + what)
        if not ok:
            self.failures += 1

    def wait(self, what, cond, timeout):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if cond():
                self.check(what, True)
                return True
            time.sleep(0.2)
        self.check(what + f" (за {timeout} с)", False)
        return False

    def daemon_pid(self):
        try:
            with open(os.path.join(self.dir, "run", "daemon.pid")) as f:
                return int(f.read().strip())
        except (OSError, ValueError):
            return 0

    def run(self):
        for t in [self.launcher, self.aliced] + [self.platform_conn] * 4:
            threading.Thread(target=t, daemon=True).start()
        self.wait("платформа подключена к mediad напрямую", lambda: self.conns() == (0, 4), 10)

        print("\n== start / status / kill -9 / stop")
        r = self.omd("start")
        self.check("start: код 0", r.returncode == 0)
        self.check("правило стоит", len(self.rules()) == 1)
        self.check("4 соединения через шим", self.conns()[0] == 4)
        self.omd("status")
        pid = self.daemon_pid()
        if pid:
            os.kill(pid, signal.SIGKILL)
            t0 = time.time()
            self.wait("kill -9: правило снято", lambda: not self.rules(), 2)
            print(f"    снято через {time.time() - t0:.2f} с")
            self.wait("страж перезапустил демон и вернул правило", lambda: self.daemon_pid() not in (0, pid) and len(self.rules()) == 1, 10)
            self.wait("reroute: платформа снова через шим", lambda: self.conns()[0] == 4, 30)
        r = self.omd("stop")
        self.check("stop: код 0", r.returncode == 0)
        self.check("правила нет", not self.rules())
        self.check("платформа напрямую", self.conns() == (0, 4))

        print("\n== install / doctor / restart / config / uninstall")
        r = self.omd("install", "-yes")
        self.check("install: код 0", r.returncode == 0)
        self.check("скрипт автозапуска записан", os.path.exists(os.path.join(self.root, "etc/init.d/S98openmediad")))
        self.check("ссылка /usr/bin/openmediad", os.path.islink(os.path.join(self.root, "usr/bin/openmediad")))
        self.check("4 соединения через шим", self.conns()[0] == 4)
        self.omd("doctor")
        r = self.omd("config", "set", "general.log_level", "trace")
        self.check("config set применён на ходу", "применены" in r.stdout)
        r = self.omd("restart")
        self.check("restart: код 0", r.returncode == 0 and self.conns()[0] == 4)
        r = self.omd("install", "-yes")
        self.check("повторный install (обновление): код 0", r.returncode == 0)
        r = self.omd("config", "get", "general.log_level", show=False)
        self.check("обновление сохранило настройки", r.stdout.strip() == '"trace"')
        r = self.omd("uninstall", "-yes")
        self.check("uninstall: код 0", r.returncode == 0)
        self.check("rootfs чистый", not os.path.lexists(os.path.join(self.root, "etc/init.d/S98openmediad"))
                   and not os.path.lexists(os.path.join(self.root, "usr/bin/openmediad")))
        self.check("правила нет, платформа напрямую", not self.rules() and self.conns() == (0, 4))

        print("\n== три быстрых падения подряд → страж сдаётся")
        os.makedirs(os.path.join(self.root, "data/opt/openmediad"), exist_ok=True)
        self.omd("start")
        conf = os.path.join(self.root, "data/opt/openmediad/openmediad.conf")
        with open(conf, "a") as f:
            f.write("\n[general]\nlog_level = \"ошибка\"\n")  # следующий запуск демона упадёт на настройках
        pid = self.daemon_pid()  # убить работающий; следующие запуски падают сами на настройках
        if pid:
            os.kill(pid, signal.SIGKILL)
        self.wait("страж сдался", lambda: open(os.path.join(self.dir, "run", "guard.state")).read().startswith("gave-up"), 20)
        self.check("правила нет", not self.rules())
        self.omd("status")
        os.remove(conf)
        self.omd("stop")

    def close(self):
        self.stop.set()
        subprocess.run([self.binary, "stop", "-no-wait"], env=self.env, capture_output=True)
        if self.mediad:
            self.mediad.terminate()
        time.sleep(0.5)
        shutil.rmtree(self.dir, ignore_errors=True)


def main():
    if len(sys.argv) != 2:
        print(__doc__)
        sys.exit(2)
    if not os.access(sys.argv[1], os.X_OK):
        sys.exit(f"нет бинарника {sys.argv[1]}: соберите go run ./tools/build -host -o {sys.argv[1]}")
    if os.geteuid() != 0:
        sys.exit("нужен root (команды установки проверяют права); сеть и система не трогаются")
    sim = Sim(sys.argv[1])
    try:
        sim.run()
    finally:
        sim.close()
    print(f"\nошибок: {sim.failures}")
    sys.exit(1 if sim.failures else 0)


if __name__ == "__main__":
    main()
