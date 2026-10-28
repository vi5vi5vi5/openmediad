package explain

// Записи журнала демона: решения автомата mediad, перехваченные команды и собственные события openmediad.

import (
	"fmt"
	"path"
	"strings"

	"openmediad/internal/journal"
	"openmediad/internal/quasar/msg"
	"openmediad/internal/system/clock"
)

// Note переводит решение автомата mediad (Machine.Note) в запись журнала с пояснением простыми словами.
func Note(kind, id, detail string) journal.Entry {
	id8 := func(id string) string { return id[:min(8, len(id))] }
	lvl, text, why := journal.Info, "", ""
	{
		switch kind {
		case "prefetch":
			text = fmt.Sprintf("предзагрузка трека «%s»", detail)
			why = "Платформа просит заранее открыть следующий трек. Шим начал скачивать и декодировать его; пока события для платформы не нужны."
		case "play-warm":
			text = fmt.Sprintf("запуск «%s»: поток уже открыт", detail)
			why = "Быстрый старт: звук уже в буфере, остаётся передать его на динамик."
		case "play-cold":
			text = fmt.Sprintf("запуск «%s»: открываю поток", detail)
			why = "Заранее открыт не был: нужно подключиться к серверу Яндекса и запустить декодер (≈0,5 с до звука)."
		case "play-resume":
			text = "возобновление с паузы: " + detail
			why = "Платформа прислала новую команду play для трека, который стоит на паузе. Берём готовый источник, заново ничего не скачиваем."
		case "pause":
			text = "пауза плеера " + id8(id)
			why = "Вывод остановлен, источник и позиция сохранены (как кэш плееров у mediad)."
		case "retire":
			text = fmt.Sprintf("плеер %s закрыт: %s", id8(id), detail)
			why = "Новый play вытеснил старый плеер; платформа получит у старого событие «закончил»."
		case "evict":
			text = fmt.Sprintf("кэш паузы плеера %s убран", id8(id))
			why = "На паузе держим один плеер (как mediad), чтобы не копить декодеры в памяти: " + detail + "."
		case "expire":
			text = fmt.Sprintf("плеер %s на паузе закрыт по времени", id8(id))
			why = "Пауза длилась дольше 30 минут (так же закрывает кэш mediad); «продолжи» теперь начнёт трек заново."
		case "release":
			text = "плеер " + id8(id) + " освобождён"
			why = "Платформа отменила предзагрузку или закрыла плеер; декодер и скачивание остановлены."
		case "focus":
			lvl = journal.Info
			if detail == "true" {
				text, why = "приглушаю музыку", "Алиса слушает или говорит (аудиофокус у диалога). Громкость музыки снижается до 25 % плавно."
			} else {
				text, why = "возвращаю громкость музыки", "Диалог закончился, громкость музыки плавно возвращается."
			}
		case "seek":
			text = fmt.Sprintf("перемотка плеера %s: %s", id8(id), detail)
			why = "Команда «перемотай»: позиция сдвинута сразу, поток откроется заново с нужного места (Range-запрос)."
		case "finished":
			text = fmt.Sprintf("плеер %s: %s", id8(id), detail)
			why = "Платформа получит событие «закончил» и сама запустит следующий трек."
		case "voice-start":
			text = "ответ Алисы: начало (плеер " + id8(id) + ")"
			why = "Платформа начинает озвучивать ответ. Шим поднял aplay и ждёт звук; настоящему mediad ответ не передан."
		case "voice-playing":
			text = "ответ Алисы: пошёл звук"
			why = "Первые отсчёты декодированного голоса переданы на динамик; платформа получила PLAYING."
		case "voice-ended":
			text = "ответ Алисы: " + detail
			why = "Звук доигран и aplay закрыт; платформа получила ENDED и пришлёт release."
		case "voice-release":
			text = "ответ Алисы: плеер " + id8(id) + " освобождён"
			why = "Платформа закрыла голосовой плеер (штатно после ENDED; раньше — если ответ перебили)."
		case "voice-failed":
			lvl = journal.Warn
			text = fmt.Sprintf("ответ Алисы %s: не удалось озвучить: %s", id8(id), detail)
			why = "Откат: все команды ответа переданы настоящему mediad, он говорит сам."
		case "file-start":
			text = "звонок: запуск " + path.Base(detail) + " (плеер " + id8(id) + ")"
			why = "Платформа просит сыграть системный звук (таймер, будильник). Шим раскодирует файл и будет играть его по кругу, пока не придёт release; mediad команду не получил."
			if !strings.HasPrefix(detail, "/") { // повтор: вместо пути — пометка автомата
				text, why = "звонок: "+detail, "Повторная стартовая команда того же плеера: звонок уже идёт, второй звук не нужен."
			}
		case "file-once":
			text = "системный звук: запуск " + path.Base(detail) + " (плеер " + id8(id) + ")"
			why = "Одноразовый звук (после «Алиса», отмена, щелчок громкости, сигнал Bluetooth; поле 1.50 = 1): шим играет файл один раз и сам сообщает платформе о конце (ENDED), release она пришлёт после. mediad команду не получил."
		case "file-playing":
			text = "плеер файлов " + id8(id) + ": пошёл звук"
			why = "Файл раскодирован и первые отсчёты ушли на динамик; платформа получила PLAYING. " + detail + "."
		case "file-ended":
			text = "системный звук " + id8(id) + ": доиграл"
			why = "Файл сыгран один раз до конца; платформа получила ENDED и сейчас пришлёт release."
		case "file-release":
			text = "плеер файлов " + id8(id) + " освобождён"
			why = "Платформа освободила плеер. У звонка это остановка (нажали кнопку, сказали «стоп» или время вышло): звук оборван, платформа получила ENDED. У одноразового звука release приходит после ENDED, и это штатный конец."
		case "file-failed":
			lvl = journal.Warn
			text = fmt.Sprintf("звонок %s: не удалось сыграть: %s", id8(id), detail)
			why = "Если звука ещё не было, стартовая команда передана настоящему mediad и он звонит сам; если звук уже шёл, звонок считается законченным."
		case "handoff":
			text = fmt.Sprintf("плеер %s: %s", id8(id), strings.TrimPrefix(detail, "шим останавливается: "))
			why = "openmediad останавливается и отдаёт плееры mediad: музыка встаёт на паузу с текущей позицией (команда «продолжи» запустит трек у mediad), ответ Алисы обрывается, звонок продолжается у mediad (ему передана стартовая команда)."
		case "failed":
			lvl = journal.Warn
			text = fmt.Sprintf("плеер %s: поток не открылся: %s", id8(id), detail)
			why = "Откат: команда play передаётся настоящему mediad, он сыграет трек сам."
		default:
			text = kind + " " + detail
		}
	}
	return journal.Entry{Lvl: lvl, Src: "player", Msg: text, Why: why, Data: map[string]any{"player": id8(id), "mono_s": float64(clock.Mono()) / 1e9}}
}

// Intercepted — запись о команде, которую перехватил openmediad (штатному mediad она не передана).
func Intercepted(connID uint64, cmd msg.AudioCommand) journal.Entry {
	ci := InfoOfCommand(cmd)
	var text, why string
	switch ci.Kind {
	case msg.CmdPrefetch:
		text, why = "перехвачена предзагрузка "+ci.Track.Title, "Команда не передана mediad: трек открывает шим."
	case msg.CmdPlay:
		text, why = "перехвачен запуск «"+ci.Track.Artist+" — "+ci.Track.Title+"»", "Команда не передана mediad: играет шим. Если поток не откроется, команда будет передана mediad."
	case msg.CmdPause:
		text, why = "перехвачена пауза плеера "+ci.Player.ID[:min(8, len(ci.Player.ID))], "Плеер ведёт шим, поэтому пауза выполнена им."
	case msg.CmdCrossfade:
		text, why = "перехвачена настройка кроссфейда", "Параметры перехода между треками запомнены, в событиях появятся поля 35 и 46."
	case msg.CmdRelease:
		text, why = "перехвачено освобождение плеера "+ci.Player.ID[:min(8, len(ci.Player.ID))], "Шим закрыл свой плеер."
	case msg.CmdFileStart:
		text, why = "перехвачен запуск звукового файла "+path.Base(ci.File), "Звонок таймера или будильника либо одноразовый системный звук: играет шим, команда не передана mediad. Если звук не пойдёт, она будет передана mediad."
	default:
		text, why = "перехвачена команда "+ci.Kind.String()+" ("+ci.Reason+")", "Команда касалась плеера шима и mediad не передана."
	}
	switch ci.Kind {
	case msg.CmdVoiceStart:
		text, why = "перехвачено начало ответа Алисы", "Голос играет шим: команда не передана mediad."
	case msg.CmdVoiceChunk:
		text, why = fmt.Sprintf("перехвачен фрагмент голоса, %d Б", ci.Size), "Сжатый звук (Opus) уходит в декодер шима."
	case msg.CmdVoiceEOS:
		text, why = "перехвачен конец данных ответа", "Все фрагменты ответа получены; шим доиграет и сообщит платформе."
	}
	d := ci.Data()
	d["conn"] = connID
	d["mono_s"] = float64(clock.Mono()) / 1e9 // CLOCK_MONOTONIC: сверка с trigger_time из pcmwatch
	lvl := journal.Info
	if ci.Kind == msg.CmdVoiceChunk {
		lvl = journal.Trace
	}
	return journal.Entry{Lvl: lvl, Src: "player", Msg: text, Why: why, Data: d}
}

// Synthesized — запись о событии плеера, которое openmediad сам отправил платформе.
func Synthesized(body []byte) journal.Entry {
	ei := InfoOfEvent(body)
	text, why := ei.Explain(true)
	return journal.Entry{Lvl: ei.Level(), Src: "player", Msg: "→платформа: " + text, Why: why, Data: ei.Data()}
}
