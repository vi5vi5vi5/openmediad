# yandex-music/aac — AAC

* `ADTS()` — заголовок ADTS для голых кадров AAC из MP4.
* `Decoder` / `NewGst()` — декодер AAC → PCM S16LE 48 кГц стерео на GStreamer колонки (`libs/gstreamer`): `fdsrc ! aacparse ! avdec_aac ! …`.

Декодера AAC на чистом Go в проекте нет (запас З5 в PLAN.md, §13.5).
