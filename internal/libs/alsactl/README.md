# libs/alsactl — регуляторы ALSA

Громкость колонки (`Master Playback Volume`) через ioctl управляющего устройства `/dev/snd/controlC0` с подпиской на изменения — без
`amixer` и без опроса; статус PCM из `/proc/asound`.

* `ctl.go`, `ctl_linux.go`, `ctl_other.go` — `Watch` (`Level`, `Changes`, `Set`), `ReadInteger`; вне 64-битного Linux — заглушка.
* `pcm.go` — `ParseStatus`, `Read`: состояние PCM и `trigger_time`.

**Кому нужно:** `bt-out` (повторять громкость колонки в наушниках), инструменты разработчика (`pcmwatch`, `mixer`).
