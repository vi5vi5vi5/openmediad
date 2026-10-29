// Package explain — безопасные описания команд и событий плеера для журнала: что это за кадр и что с ним сделал движок,
// простыми словами. Секреты (ключ потока, подписанный URL, JSON директив, содержимое голосовых чанков) сюда не попадают:
// берутся только идентификатор и название трека, исполнитель, хост CDN, размеры и состояния.
package explain

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"openmediad/internal/core/journal"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
)

// Что можно показывать, а что нельзя: в записи идут идентификатор и название трека, исполнитель, хост CDN, размеры,
// состояния. Не идут: ключ расшифровки (поле 39 команды), URL целиком (в нём подпись доступа), JSON директивы
// (поле 7: идентификаторы запросов аккаунта), содержимое голосовых чанков.

// CommandInfo — безопасное описание команды плееру: что это, чья, о каком треке.
type CommandInfo struct {
	Kind   msg.CommandKind
	Player msg.Player
	Track  msg.Track
	Host   string // хост CDN потока (без пути и подписи)
	Focus  int    // для focus: 1 — диалог занял фокус, 0 — отдал; -1 — не focus
	Size   int    // размер команды, Б
	Reason string
	File   string // для file-start: путь к звуковому файлу (системные звуки не секрет)
	Loops  bool   // для file-start: файл звучит по кругу до release (звонок), иначе играет один раз
}

// InfoOfCommand разбирает команду плееру.
func InfoOfCommand(c msg.AudioCommand) CommandInfo {
	ci := CommandInfo{Kind: c.Classify(), Player: c.Player(), Focus: -1, Size: len(c), Reason: c.Reason()}
	if t, ok := c.Track(); ok {
		ci.Track = t
		ci.Host = hostOf(t.URL)
	}
	if ci.Kind == msg.CmdFocus {
		v, _ := pb.Uint(c, 39)
		ci.Focus = int(v)
	}
	if ci.Kind == msg.CmdFileStart {
		ci.File, _ = c.FilePath()
		ci.Loops = c.FileLoops()
	}
	return ci
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// Data — поля для записи журнала (без секретов).
func (ci CommandInfo) Data() map[string]any {
	d := map[string]any{"kind": ci.Kind.String(), "bytes": ci.Size}
	if ci.Player.ID != "" {
		d["player"] = ci.Player.Kind.String() + "/" + short(ci.Player.ID)
	}
	if ci.Track.ID != "" {
		d["track"] = ci.Track.ID
	}
	if ci.Track.Title != "" {
		d["title"] = strings.TrimSpace(ci.Track.Artist + " — " + ci.Track.Title)
	}
	if ci.Track.OffsetMs > 0 {
		d["offset_ms"] = ci.Track.OffsetMs
	}
	if ci.Host != "" {
		d["cdn"] = ci.Host
	}
	if ci.Reason != "" {
		d["reason"] = ci.Reason
	}
	if ci.File != "" {
		d["file"] = ci.File
		d["file_loops"] = ci.Loops
	}
	return d
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (ci CommandInfo) label() string {
	if ci.Track.Title != "" {
		return fmt.Sprintf("«%s — %s»", ci.Track.Artist, ci.Track.Title)
	}
	if ci.Player.ID != "" {
		return "плеер " + short(ci.Player.ID)
	}
	return ""
}

// Explain возвращает короткое сообщение и пояснение для команды, переданной настоящему mediad (шим её не перехватывал).
func (ci CommandInfo) Explain() (text, why string) {
	switch ci.Kind {
	case msg.CmdVoiceStart:
		return "голос Алисы: начало ответа", "Алиса собирается говорить. Голос играет настоящий mediad (компонент tts выключен или ответ отдан ему), шим только пропускает кадры."
	case msg.CmdVoiceChunk:
		return fmt.Sprintf("голос Алисы: фрагмент звука, %d Б", ci.Size), "Сжатый звук (Opus) фразы Алисы. Декодирует и играет настоящий mediad."
	case msg.CmdVoiceEOS:
		return "голос Алисы: данные кончились", "Все фрагменты фразы переданы; плеер доиграет и сообщит о конце."
	case msg.CmdRelease:
		return "освобождение плеера " + short(ci.Player.ID), "Плеер больше не нужен. Для голоса Алисы это нормальный конец фразы."
	case msg.CmdFileStart:
		if !ci.Loops {
			return "системный звук: " + path.Base(ci.File), "Одноразовый звук (после «Алиса», щелчок громкости, сигнал Bluetooth). Играет настоящий mediad (компонент sounds выключен или звук отдан ему) и сам сообщает о конце, шим только пропускает кадры."
		}
		return "звонок: запуск файла " + path.Base(ci.File), "Таймер или будильник просит сыграть системный звук. Играет настоящий mediad (компонент sounds выключен или звонок отдан ему), шим только пропускает кадры."
	case msg.CmdFocus:
		if ci.Focus == 1 {
			return "аудиофокус: диалог занял", "Алиса слушает или говорит. Шим пересылает команду mediad и приглушает свою музыку."
		}
		return "аудиофокус: диалог отдал", "Диалог закончился. Шим возвращает громкость своей музыки."
	case msg.CmdEqualizer:
		return "эквалайзер: новая настройка", "Настройка передана mediad. Музыку шима она пока не меняет (свой эквалайзер не сделан)."
	case msg.CmdOther:
		if ci.Reason != "" {
			return "служебная команда: " + ci.Reason, "Не относится к воспроизведению; передана mediad как есть."
		}
		return "служебная команда плееру", "Передана mediad как есть."
	case msg.CmdPlay, msg.CmdPrefetch, msg.CmdPause, msg.CmdCrossfade:
		return fmt.Sprintf("%s %s передана mediad", ci.Kind, ci.label()), "Шим эту команду не перехватил (компонент yandex выключен либо плеер ведёт mediad), mediad выполнит её сам."
	}
	return fmt.Sprintf("команда плееру (%s)", ci.Kind), ""
}

// EventInfo — безопасное описание события плеера.
type EventInfo struct {
	Kind   msg.EventKind
	State  msg.PlayState
	Player msg.Player
	PosMs  uint64
	Track  string
	Title  string
}

// InfoOfEvent разбирает событие плеера (тело сообщения типа 127).
func InfoOfEvent(body []byte) EventInfo {
	e := msg.AudioEvent(body)
	ei := EventInfo{Kind: e.Kind(), State: e.State(), Player: e.Player(), Track: e.TrackID()}
	ei.PosMs, _ = e.PositionMs()
	if d, ok := pb.Bytes(body, 3); ok {
		if md, ok := pb.Bytes(d, 9); ok {
			t, _ := pb.Str(md, 1)
			a, _ := pb.Str(md, 2)
			if t != "" {
				ei.Title = strings.TrimSpace(a + " — " + t)
			}
		}
	}
	return ei
}

// Level — уровень записи: прогресс, диагностика и flag идут раз в секунду и считаются фоном.
func (ei EventInfo) Level() string {
	if ei.Kind == 1 {
		return journal.Info
	}
	return journal.Trace
}

func (ei EventInfo) Data() map[string]any {
	d := map[string]any{"event": ei.Kind.String(), "state": ei.State.String(), "player": ei.Player.Kind.String() + "/" + short(ei.Player.ID), "pos_ms": ei.PosMs}
	if ei.Track != "" {
		d["track"] = ei.Track
	}
	if ei.Title != "" {
		d["title"] = ei.Title
	}
	return d
}

// Explain — сообщение и пояснение для события.
func (ei EventInfo) Explain(synthesized bool) (text, why string) {
	who := ei.Player.Kind.String() + " " + short(ei.Player.ID)
	pos := fmt.Sprintf("%.1f с", float64(ei.PosMs)/1000)
	by := "Событие пришло от настоящего mediad."
	if synthesized {
		by = "Событие собрал шим (mediad этот плеер не ведёт); платформа читает его так же, как от mediad."
	}
	switch ei.Kind {
	case 1:
		switch ei.State {
		case 1:
			return fmt.Sprintf("%s: запускается (%s)", who, pos), "Плеер принял команду play и готовится. " + by
		case 3:
			return fmt.Sprintf("%s: играет с %s", who, pos), "Звук пошёл. " + by
		case 4:
			return fmt.Sprintf("%s: пауза на %s", who, pos), "Воспроизведение остановлено, плеер ждёт «продолжи». " + by
		case 5:
			return fmt.Sprintf("%s: закончил на %s", who, pos), "Плеер завершил работу (дошёл до конца, остановлен или вытеснен другим). Платформа по этому событию решает, что играть дальше. " + by
		}
	case 2:
		return fmt.Sprintf("%s: позиция %s", who, pos), "Отчёт о ходе воспроизведения раз в ≈1 с. " + by
	case 5:
		return who + ": диагностика загрузки", "Служебные счётчики. " + by
	case 6:
		return who + ": смена громкостного режима (flag)", "Приходит при старте и когда диалог берёт или отдаёт фокус. " + by
	}
	return who + ": событие", by
}

// DescribeFrame готовит запись для кадра шины, прошедшего через шим как есть. ok=false — кадр не стоит показывать.
func DescribeFrame(toService bool, frame []byte) (e journal.Entry, ok bool) {
	m, err := wire.Unwrap(frame)
	if err != nil {
		return journal.Entry{Lvl: journal.Warn, Src: "audioclient", Msg: fmt.Sprintf("кадр без конверта, %d Б", len(frame)), Why: "Не похоже на сообщение шины Quasar; шим переслал его как есть."}, true
	}
	switch m.Type {
	case wire.TypeAudioCommand:
		ci := InfoOfCommand(msg.AudioCommand(m.Body))
		text, why := ci.Explain()
		lvl := journal.Info
		if ci.Kind == msg.CmdVoiceChunk || ci.Kind == msg.CmdEqualizer {
			lvl = journal.Trace
		}
		return journal.Entry{Lvl: lvl, Src: "audioclient", Msg: "→mediad: " + text, Why: why, Data: ci.Data()}, true
	case wire.TypeAudioEvent:
		ei := InfoOfEvent(m.Body)
		text, why := ei.Explain(false)
		return journal.Entry{Lvl: ei.Level(), Src: "audioclient", Msg: "←mediad: " + text, Why: why, Data: ei.Data()}, true
	}
	dir := "←"
	if toService {
		dir = "→"
	}
	return journal.Entry{Lvl: journal.Trace, Src: "audioclient", Msg: fmt.Sprintf("%s кадр типа %s, %d Б", dir, m.Type, len(frame)), Data: map[string]any{"type": m.Type.String(), "bytes": len(frame)}}, true
}
