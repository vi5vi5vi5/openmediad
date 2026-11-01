package observe

import "testing"

// Окончательная фраза приходит в нескольких состояниях Алисы подряд (LISTENING, потом BUSY), а отдаваться должна один раз на запрос.
func TestFirstPhraseOncePerRequest(t *testing.T) {
	o := &Observer{}
	if !o.firstPhrase("req-1") {
		t.Fatal("первая фраза запроса не отдана")
	}
	if o.firstPhrase("req-1") {
		t.Fatal("фраза того же запроса отдана второй раз")
	}
	if !o.firstPhrase("req-2") {
		t.Fatal("новый запрос не отдан")
	}
}

func TestFirstPhraseMemoryIsBounded(t *testing.T) {
	o := &Observer{}
	for i := 0; i < 100; i++ {
		o.firstPhrase(string(rune('a'+i%26)) + string(rune('A'+i/26)))
	}
	if n := len(o.phrased); n > 16 {
		t.Fatalf("помним %d запросов, предел 16", n)
	}
}
