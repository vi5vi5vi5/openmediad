package mediad

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"testing"

	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
	"openmediad/internal/devtools/capture"
)

// plainSnapFrom вынимает из настоящего события голоса входные данные сборщика.
func plainSnapFrom(ev []byte) PlainSnap {
	var s PlainSnap
	s.Kind = msg.AudioEvent(ev).Kind()
	s.State = msg.AudioEvent(ev).State()
	d, _ := pb.Bytes(ev, 3)
	s.PosMs, _ = pb.Uint(d, 28)
	s.Idx, _ = pb.Uint(d, 12)
	s.Has6 = pb.Has(d, 6)
	s.Dur6, _ = pb.Uint(d, 6)
	s.Mono, _ = pb.Uint(d, 29)
	s.UUID, _ = pb.Str(d, 22)
	s.Start, _ = pb.Uint(ev, 5)
	s.Stop, _ = pb.Uint(ev, 7)
	if v, ok := pb.Uint(ev, 9); ok {
		s.HasT9, s.T9 = true, v == 1
	}
	if v, ok := pb.Uint(ev, 17); ok {
		s.HasT17, s.T17 = true, v == 1
	}
	r := pb.NewReader(ev)
	for {
		f, err := r.Next()
		if err != nil {
			break
		}
		if f.Num != 14 {
			continue
		}
		k, _ := pb.Str(f.B, 1)
		switch k {
		case "mark":
			s.Mark, _ = pb.Str(f.B, 2)
		case "vsid":
			s.Vsid, _ = pb.Str(f.B, 2)
		case "rss_mem":
			s.RSS, _ = pb.Uint(f.B, 3)
		case "bitrate":
			s.Bitrate, _ = pb.Uint(f.B, 3)
		case "played_sec":
			s.PlayedSec, _ = pb.Uint(f.B, 3)
			s.HasPlayed = true
		}
	}
	return s
}

// TestVoiceEventsMatchRecordings: событие голоса собирается из команды voice-start и входов (позиция, uuid, метки, diag)
// байт в байт. Свои события шима (rss_mem = 33000) пропускаются.
func TestVoiceEventsMatchRecordings(t *testing.T) {
	files := recordings(t)
	total, bad := 0, map[string]int{}
	for _, path := range files {
		starts := map[string]msg.AudioCommand{}
		err := capture.WalkFile(path, func(fr capture.Frame) {
			if fr.Svc != "audioclient" {
				return
			}
			m, err := wire.Unwrap(fr.Data)
			if err != nil {
				return
			}
			switch m.Type {
			case wire.TypeAudioCommand:
				c := msg.AudioCommand(append([]byte(nil), m.Body...))
				if c.Classify() == msg.CmdVoiceStart {
					starts[c.Player().ID] = c
				}
			case wire.TypeAudioEvent:
				e := msg.AudioEvent(m.Body)
				p := e.Player()
				if p.Kind != msg.PlayerVoice || e.Kind() == 5 || isSynthetic(m.Body) {
					return
				}
				c, ok := starts[p.ID]
				if !ok {
					return
				}
				sn := plainSnapFrom(m.Body)
				// правила, по которым автомат решает, что класть в событие (их голый «вход» сверка выше не проверяет):
				if sn.HasPlayed != (sn.PosMs >= 1000) {
					bad["played_sec присутствует не так, как «pos ≥ 1000»"]++
				}
				if sn.Idx != sn.PosMs/1000 {
					bad["секунды в поле 12 не равны pos/1000"]++
				}
				// 6 и 29 есть у ENDED и у progress/flag в состоянии PLAYING; нет у STARTING, PLAYING-как-смены-состояния и у flag в STARTING
				if sn.Has6 != (sn.State == 5 || (sn.Kind != 1 && sn.State == 3)) {
					bad[fmt.Sprintf("поле 6 не так: вид %v состояние %v есть=%v", e.Kind(), e.State(), sn.Has6)]++
				}
				got := PlainMetaFrom(c).Event(sn)
				total++
				if !bytes.Equal(got, m.Body) {
					bad[fmt.Sprintf("%v/%v %s", e.Kind(), e.State(), firstDiff(got, m.Body, ""))]++
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if total == 0 {
		t.Fatal("в записях нет событий голоса — проверка вхолостую")
	}
	t.Logf("событий голоса сверено: %d, не совпало: %d", total, sum(bad))
	var keys []string
	for k := range bad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("расхождение %-40s × %d", k, bad[k])
	}
}

// TestFileEventsMatchRecordings: события плеера файлов (звонок таймера и будильника) собираются из стартовой команды и входов
// байт в байт; заодно проверяются правила наличия полей, по которым автомат решает, что класть в событие.
func TestFileEventsMatchRecordings(t *testing.T) {
	files := recordings(t)
	total, releases, bad := 0, 0, map[string]int{}
	stats := map[string]int{}
	for _, path := range files {
		starts := map[string]msg.AudioCommand{}
		afterPlaying := map[string]bool{}                             // следующий progress в PLAYING у этого плеера — первый
		prevBody, prevFirst := map[string][]byte{}, map[string]bool{} // платформа рассылает событие по четырём соединениям: копии идут подряд
		err := capture.WalkFile(path, func(fr capture.Frame) {
			if fr.Svc != "audioclient" {
				return
			}
			m, err := wire.Unwrap(fr.Data)
			if err != nil {
				return
			}
			switch m.Type {
			case wire.TypeAudioCommand:
				c := msg.AudioCommand(append([]byte(nil), m.Body...))
				if c.Classify() == msg.CmdFileStart && c.FileLoops() { // одноразовые звуки (50:1) автомат не играет: правила только для звонков
					starts[c.Player().ID] = c
				}
				if c.Player().Kind == msg.PlayerFile && c.Classify() == msg.CmdRelease {
					releases++ // автомат ловит release звонка по этому же признаку
				}
			case wire.TypeAudioEvent:
				e := msg.AudioEvent(m.Body)
				p := e.Player()
				if p.Kind != msg.PlayerFile || e.Kind() == 5 || isSynthetic(m.Body) {
					return
				}
				c, ok := starts[p.ID]
				if !ok {
					return
				}
				sn := plainSnapFrom(m.Body)
				stats[fmt.Sprintf("%v/%v 6=%v(%d) 29=%v bitrate=%d played=%v t9=%v/%v t17=%v/%v", e.Kind(), e.State(), sn.Has6, sn.Dur6, sn.Mono > 0, sn.Bitrate, sn.HasPlayed, sn.HasT9, sn.T9, sn.HasT17, sn.T17)]++
				got := PlainMetaFrom(c).Event(sn)
				total++
				if !bytes.Equal(got, m.Body) {
					bad[fmt.Sprintf("%v/%v %s", e.Kind(), e.State(), firstDiff(got, m.Body, ""))]++
				}
				// таблица правил, по которой события строит автомат (file_machine.go), должна сходиться с настоящими
				first := prevFirst[p.ID]
				if !bytes.Equal(prevBody[p.ID], m.Body) {
					first = e.Kind() == 2 && e.State() == 3 && afterPlaying[p.ID]
					if e.Kind() == 1 && e.State() == 3 {
						afterPlaying[p.ID] = true
					} else if e.Kind() == 2 && e.State() == 3 {
						afterPlaying[p.ID] = false
					}
					prevBody[p.ID], prevFirst[p.ID] = append([]byte(nil), m.Body...), first
				}
				if v := fileRuleViolation(sn, first); v != "" {
					bad["правило: "+v]++
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if total == 0 {
		t.Fatal("в записях нет событий плеера файлов — проверка вхолостую")
	}
	if releases == 0 {
		t.Fatal("в записях нет команд release плеера файлов: не проверено, что Classify их узнаёт")
	}
	t.Logf("событий плеера файлов сверено: %d, не совпало: %d", total, sum(bad))
	var ks []string
	for k := range stats {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		t.Logf("  %-70s × %d", k, stats[k])
	}
	var keys []string
	for k := range bad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Errorf("расхождение %-40s × %d", k, bad[k])
	}
}

// fileTrack — события одного плеера файлов из записи: стартовая команда и настоящие события без копий (платформа рассылает
// каждое по четырём соединениям, а запись со стороны шима видит обе его стороны).
type fileTrack struct {
	start msg.AudioCommand
	evs   [][]byte // без diag
	diags [][]byte
	seen  map[string]bool
}

// fileTrackCache — разбор записей долгий (сотни мегабайт), а нужен трём тестам.
var fileTrackCache = map[bool][]*fileTrack{}

// fileTracks собирает плееры файлов из всех записей. once — одноразовые звуки (50:1) или звонки (50:0).
func fileTracks(t *testing.T, once bool) (tracks []*fileTrack) {
	if c, ok := fileTrackCache[once]; ok {
		return c
	}
	defer func() { fileTrackCache[once] = tracks }()
	for _, path := range recordings(t) {
		byID := map[string]*fileTrack{}
		err := capture.WalkFile(path, func(fr capture.Frame) {
			if fr.Svc != "audioclient" {
				return
			}
			m, err := wire.Unwrap(fr.Data)
			if err != nil {
				return
			}
			switch m.Type {
			case wire.TypeAudioCommand:
				c := msg.AudioCommand(append([]byte(nil), m.Body...))
				if c.Classify() == msg.CmdFileStart && c.FileLoops() != once && byID[c.Player().ID] == nil {
					tr := &fileTrack{start: c, seen: map[string]bool{}}
					byID[c.Player().ID] = tr
					tracks = append(tracks, tr)
				}
			case wire.TypeAudioEvent:
				tr := byID[msg.AudioEvent(m.Body).Player().ID]
				if tr == nil || isSynthetic(m.Body) || tr.seen[string(m.Body)] {
					return
				}
				tr.seen[string(m.Body)] = true
				body := append([]byte(nil), m.Body...)
				if msg.AudioEvent(body).Kind() == 5 {
					tr.diags = append(tr.diags, body)
				} else {
					tr.evs = append(tr.evs, body)
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return tracks
}

// TestFileOnceEventsMatchRecordings: события одноразовых системных звуков (50:1) у настоящего mediad собираются из стартовой
// команды и входов байт в байт, подчиняются таблице fileOnceRuleViolation и правилу uuid; позиция всегда 0, ENDED приходит
// сам, release — после него.
func TestFileOnceEventsMatchRecordings(t *testing.T) {
	tracks := fileTracks(t, true)
	total, bad := 0, map[string]int{}
	files := map[string]int{}
	for _, tr := range tracks {
		meta := PlainMetaFrom(tr.start)
		p, _ := tr.start.FilePath()
		files[p[strings.LastIndex(p, "/")+1:]]++
		var kinds []string
		for _, ev := range tr.evs {
			total++
			sn := plainSnapFrom(ev)
			if got := meta.Event(sn); !bytes.Equal(got, ev) {
				bad["сборка "+firstDiff(got, ev, "")]++
			}
			if v := fileOnceRuleViolation(sn); v != "" {
				bad["правило: "+v]++
			}
			if sn.PosMs != 0 {
				bad["позиция не 0"]++
			}
			kinds = append(kinds, fmt.Sprintf("%v/%v", msg.AudioEvent(ev).Kind(), msg.AudioEvent(ev).State()))
		}
		if v := uuidRuleViolation(tr.evs); v != "" {
			bad["uuid: "+v]++
		}
		seq := strings.Join(kinds, " ")
		seq = strings.Replace(seq, "flag/PLAYING flag/PLAYING", "flag/PLAYING", 1) // mediad иногда шлёт flag дважды
		seq = strings.ReplaceAll(seq, " flag/ENDED", "")                           // и изредка — после ENDED (под нагрузкой)
		if seq != "state/STARTING state/PLAYING progress/PLAYING flag/PLAYING state/ENDED" &&
			seq != "state/STARTING state/PLAYING progress/PLAYING state/ENDED" {
			bad["порядок: "+seq]++
		}
	}
	if total == 0 {
		t.Skip("в записях нет одноразовых звуков, сыгранных mediad")
	}
	t.Logf("одноразовых звуков: %d %v, событий сверено: %d, не совпало: %d", len(tracks), files, total, sum(bad))
	for k, n := range bad {
		t.Errorf("расхождение %-50s × %d", k, n)
	}
}

// TestFileUUIDRuleHoldsForRings: у звонков (50:0) mediad тоже держит один uuid на все события после STARTING.
func TestFileUUIDRuleHoldsForRings(t *testing.T) {
	tracks := fileTracks(t, false)
	n, bad := 0, 0
	for _, tr := range tracks {
		if len(tr.evs) < 3 {
			continue
		}
		n++
		if v := uuidRuleViolation(tr.evs); v != "" {
			bad++
			t.Errorf("звонок %s: %s", tr.start.Player().ID[:8], v)
		}
	}
	if n == 0 {
		t.Skip("в записях нет звонков mediad")
	}
	t.Logf("звонков проверено: %d, нарушений: %d", n, bad)
}

// TestFileDiagMatchesRecordings: событие diag (вид 5) плеера файлов — и у звонков, и у одноразовых звуков — собирается Diag
// байт в байт; битрейт в нём — средний по Xing/Info (system-sounds/mp3.AvgBitrate), и приходит он один раз за плеер.
func TestFileDiagMatchesRecordings(t *testing.T) {
	total, bad := 0, map[string]int{}
	bitrates := map[uint64]int{}
	for _, once := range []bool{false, true} {
		for _, tr := range fileTracks(t, once) {
			meta := PlainMetaFrom(tr.start)
			for _, ev := range tr.diags {
				total++
				sn := plainSnapFrom(ev)
				bitrates[sn.Bitrate]++
				if got := meta.Diag(sn); !bytes.Equal(got, ev) {
					bad["сборка "+firstDiff(got, ev, "")]++
				}
				if sn.Bitrate == 0 {
					bad["diag без битрейта"]++
				}
			}
			if len(tr.diags) > 1 {
				bad["diag больше одного раза"]++
			}
		}
	}
	if total == 0 {
		t.Skip("в записях нет diag плеера файлов")
	}
	t.Logf("diag сверено: %d, битрейты %v, не совпало: %d", total, bitrates, sum(bad))
	for k, n := range bad {
		t.Errorf("расхождение %-40s × %d", k, n)
	}
}
