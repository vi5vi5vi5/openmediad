package daemon

import (
	"testing"

	"openmediad/internal/core/ctlsock"
	"openmediad/internal/core/quasar/msg"
	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/shim"
	"openmediad/internal/core/quasar/wire"
)

func playerField(kind uint64, id string) []byte {
	return pb.AppendString(pb.AppendUint(nil, 1, kind), 2, id)
}

// body — тело кадра без длины: таким его получают intercept и tap.
func body(t *testing.T, typ wire.Type, b []byte) []byte {
	t.Helper()
	frame := wire.Wrap(typ, b)
	if _, err := wire.Unwrap(frame); err == nil {
		return frame // Wrap отдаёт кадр без длины
	}
	return frame[4:]
}

// stockEvent — событие плеера от штатного mediad: вид события, состояние, вид плеера.
func stockEvent(t *testing.T, kind, state, player uint64) []byte {
	b := pb.AppendUint(nil, 1, kind)
	b = pb.AppendUint(b, 2, state)
	b = pb.AppendBytes(b, 4, playerField(player, "p1"))
	return body(t, wire.TypeAudioEvent, b)
}

func byName(list []ctlsock.StockCount) map[string]uint64 {
	out := map[string]uint64{}
	for _, x := range list {
		out[x.What] = x.N
	}
	return out
}

// Учёт: что штатный mediad сыграл сам (событие PLAYING от него), какие команды движок пропустил и какие вернул.
func TestStockUseCounts(t *testing.T) {
	s := newStockUse()
	s.fromStock(stockEvent(t, 1, 3, 1)) // музыка заиграла
	s.fromStock(stockEvent(t, 1, 3, 1))
	s.fromStock(stockEvent(t, 1, 3, 2)) // голос
	s.fromStock(stockEvent(t, 2, 3, 1)) // progress — не старт
	s.fromStock(stockEvent(t, 1, 5, 1)) // ENDED — не старт
	s.fromStock([]byte{0xff, 0xff})     // мусор
	s.passed(msg.AudioCommand(pb.AppendUint(nil, 39, 1)))
	s.passed(msg.AudioCommand(pb.AppendBytes(pb.AppendString(nil, 31, ""), 22, playerField(2, "v")))) // release голоса
	s.returned(pb.AppendBytes(pb.AppendString(nil, 30, ""), 22, playerField(2, "v")))                 // voice-eos, возвращён
	got := s.snapshot()
	if p := byName(got.Played); len(p) != 2 || p["музыка и потоки"] != 2 || p["голос Алисы"] != 1 {
		t.Fatalf("сыграл сам: %+v", got.Played)
	}
	if got.Played[0].What != "музыка и потоки" {
		t.Fatalf("самое частое — первым: %+v", got.Played)
	}
	if p := byName(got.Passed); len(p) != 2 || p["focus"] != 1 || p["release (голос Алисы)"] != 1 {
		t.Fatalf("пропущено: %+v", got.Passed)
	}
	if len(got.Returned) != 1 || got.Returned[0].What != "voice-eos (голос Алисы)" || got.Returned[0].N != 1 {
		t.Fatalf("возвращено: %+v", got.Returned)
	}
}

// Демон считает команду, которую автомат не взял, и событие «играет» от штатного mediad; счётчики видны в status.
func TestDaemonCountsWhatStockPlayerDoes(t *testing.T) {
	d := testDaemon(t)
	d.stock = newStockUse()
	d.comps = []*component{}
	conn := &shim.Conn{}
	if d.intercept(conn, body(t, wire.TypeAudioCommand, pb.AppendUint(nil, 39, 1))) {
		t.Fatal("команду фокуса движок не забирает")
	}
	d.tap(conn, shim.ToClient, stockEvent(t, 1, 3, 1), 0)
	d.tap(conn, shim.ToService, stockEvent(t, 1, 3, 1), 0) // это не от mediad: не считается
	st := d.status(false)
	if st.Stock == nil || len(st.Stock.Passed) != 1 || st.Stock.Passed[0].What != "focus" {
		t.Fatalf("status: %+v", st.Stock)
	}
	if len(st.Stock.Played) != 1 || st.Stock.Played[0].What != "музыка и потоки" || st.Stock.Played[0].N != 1 {
		t.Fatalf("status, сыграл сам: %+v", st.Stock.Played)
	}
}
