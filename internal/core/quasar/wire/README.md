# quasar/wire — кадры и конверт

* `wire.go` — кадры `[u32 LE длина][сообщение]` (`FrameReader`, `WriteFrame`), конверт `{252: тело, 253: тип}` (`Wrap` / `Unwrap`),
  типы сообщений (`TypeAudioCommand = 8`, `TypeAudioEvent = 127`, `TypeIOHub = 85` …).
* Сборка исходящих запросов к aliced: `PausePlayback`, `TogglePlayback`, `NextTrack`, `PrevTrack`, `TextInput` («сказать» фразу),
  `CancelDialog`, `RegisterEndpoint`.
