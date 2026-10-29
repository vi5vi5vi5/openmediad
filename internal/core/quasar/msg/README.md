# quasar/msg — типизированные «окна» на сообщения

Читаются только поля, подтверждённые живым трафиком; всё остальное остаётся сырым.

* `AliceState` — состояние Алисы (`FinalPhrase`, `Reply`).
* `AudioCommand` — команда плееру: `Classify` (prefetch / play / pause / release / focus / voice-* / file-start …), `Track`, `FileLoops`, `FilePath`.
* `AudioEvent`, `Player`, `Handshake`, `Remoting`.
