package spotifysoloist

import (
	"context"
	"strings"
	"testing"
)

func TestFuzzyWords(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"loser", "looser", true}, // опечатка распознавателя
		{"impala", "impalla", true},
		{"tame", "tame", true},
		{"кино", "кино", true},
		{"нойз", "нойс", true},
		{"queen", "quen", true},
		{"cat", "bat", false}, // слишком короткие: одна буква решает всё
		{"loser", "winner", false},
		{"музыка", "музей", false},
	} {
		if got := fuzzyEq(c.a, c.b); got != c.want {
			t.Errorf("fuzzyEq(%q, %q) = %v, ждали %v", c.a, c.b, got, c.want)
		}
	}
}

func TestWordsKeepsFillerWordsOfTitles(t *testing.T) {
	got := words("Давай, слушай: Ёлка & Co.")
	want := []string{"давай", "слушай", "елка", "co"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("words = %v, ждали %v (служебные слова бывают частью названия песни)", got, want)
	}
}

func TestCleanTitleAndFirstArtist(t *testing.T) {
	for in, want := range map[string]string{
		"Loser (Remastered 2012)": "Loser", "Loser - Radio Edit": "Loser", "Loser — Single": "Loser", "Song feat. Someone": "Song", "Plain": "Plain",
		"Bohemian Rhapsody [Live]": "Bohemian Rhapsody",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, ждали %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Tame Impala": "tame impala", "Tame Impala, Someone": "tame impala", "A & B": "a", "Кино feat. Х": "кино", "Нойз МС и Х": "нойз мс", "": "",
	} {
		if got := firstArtist(in); got != want {
			t.Errorf("firstArtist(%q) = %q, ждали %q", in, got, want)
		}
	}
}

func TestTitleInQuery(t *testing.T) {
	q := words("looser tame impala")
	if !titleInQuery(q, "Loser") {
		t.Error("название «Loser» слышно в запросе «looser tame impala»")
	}
	if !titleInQuery(words("группа крови кино"), "Группа крови (Remastered)") {
		t.Error("пояснение в скобках мешает")
	}
	if titleInQuery(words("tame impala"), "The Less I Know The Better") {
		t.Error("облако выбрало случайный трек исполнителя, а названия в запросе нет")
	}
	if titleInQuery(q, "") || titleInQuery(q, "Я") {
		t.Error("пустое или слишком короткое название принято")
	}
	if titleInQuery(words("tame impala"), "Tame Impala Currents") {
		t.Error("не все слова названия есть в запросе")
	}
}

func TestMatchScoreOrdering(t *testing.T) {
	q := words("looser tame impala")
	track := matchScore(q, "Loser", "Tame Impala")
	artist := matchScore(q, "Tame Impala")
	other := matchScore(q, "Elephant", "Tame Impala")
	if !(track > artist && artist > other-0.2 && track > other) {
		t.Fatalf("оценки: трек %.2f, исполнитель %.2f, чужой трек исполнителя %.2f", track, artist, other)
	}
	q = words("tame impala")
	if a, tr := matchScore(q, "Tame Impala"), matchScore(q, "Loser", "Tame Impala"); a <= tr {
		t.Fatalf("«tame impala»: исполнитель %.2f должен обгонять трек %.2f", a, tr)
	}
	if matchScore(nil, "x") != 0 {
		t.Fatal("пустой запрос получил оценку")
	}
}

const tameBody = `{
 "artists":{"items":[{"uri":"spotify:artist:AAAAAAAAAAAAAAAAAAAAAA","name":"Tame Impala"}]},
 "tracks":{"items":[
   {"uri":"spotify:track:TTTTTTTTTTTTTTTTTTTT01","name":"The Less I Know The Better","artists":[{"name":"Tame Impala"}]},
   {"uri":"spotify:track:TTTTTTTTTTTTTTTTTTTT02","name":"Loser","artists":[{"name":"Tame Impala"}]},
   {"uri":"spotify:track:TTTTTTTTTTTTTTTTTTTT03","name":"Loser","artists":[{"name":"Beck"}]}]},
 "albums":{"items":[{"uri":"spotify:album:DDDDDDDDDDDDDDDDDDDD01","name":"Currents","artists":[{"name":"Tame Impala"}]}]},
 "playlists":{"items":[]}
}`

// Из жизни: «включи looser tame impala» включало просто Tame Impala.
func TestPickSpecificTrackOverTheArtist(t *testing.T) {
	w, _ := searchRig(t, tameBody)
	res, err := w.search(context.Background(), "looser tame impala", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind  playKind
		query string
		want  string
	}{
		{kindAny, "looser tame impala", "spotify:track:TTTTTTTTTTTTTTTTTTTT02"},
		{kindTrack, "looser tame impala", "spotify:track:TTTTTTTTTTTTTTTTTTTT02"}, // не первый в выдаче, а лучший по словам
		{kindAny, "tame impala", "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA"},
		{kindAny, "currents", "spotify:album:DDDDDDDDDDDDDDDDDDDD01"},
		{kindArtist, "tame impala", "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA"},
	} {
		h, ok := pick(c.kind, c.query, res)
		if !ok || h.URI != c.want {
			t.Errorf("pick(%v, %q) = %+v; ждали %s", c.kind, c.query, h, c.want)
		}
	}
	if len(res.Tracks[1].Artists) != 1 || res.Tracks[1].Artists[0] != "Tame Impala" {
		t.Fatalf("исполнители трека не разобраны: %+v", res.Tracks[1])
	}
}

func TestFindTrackMatchesCloudChoice(t *testing.T) {
	w, _ := searchRig(t, tameBody)
	res, _ := w.search(context.Background(), "x", 5)
	h, ok := findTrack(res.Tracks, "Loser (Remastered 2012)", "Tame Impala")
	if !ok || h.URI != "spotify:track:TTTTTTTTTTTTTTTTTTTT02" {
		t.Fatalf("трек облака: %+v %v", h, ok)
	}
	if h, ok := findTrack(res.Tracks, "Loser", "Beck"); !ok || h.URI != "spotify:track:TTTTTTTTTTTTTTTTTTTT03" {
		t.Fatalf("тот же заголовок другого исполнителя: %+v %v", h, ok)
	}
	if _, ok := findTrack(res.Tracks, "Loser", "Metallica"); ok {
		t.Fatal("трек чужого исполнителя принят")
	}
	if _, ok := findTrack(res.Tracks, "Nonexistent", "Tame Impala"); ok {
		t.Fatal("несуществующий трек найден")
	}
}

// Облако выбрало трек «Loser» — включаем именно его, даже если запрос распознан как «looser tame impala».
func TestCloudTrackSelectsTheSpecificTrack(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.searchJSON = tameBody
	f.mu.Unlock()
	r.state(t, "paused")
	r.c.InterceptPhrase(reqID, "Включи looser tame impala", false)
	r.c.OnReplyTrack(reqID, "Loser", "Tame Impala")
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, "spotify:track:TTTTTTTTTTTTTTTTTTTT02") {
		t.Fatalf("Soloist получил %s", m)
	}
}

// Просили исполнителя, облако включило случайный его трек: подмена на этот трек была бы ошибкой.
func TestCloudRandomTrackDoesNotOverrideAnArtistRequest(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.searchJSON = tameBody
	f.mu.Unlock()
	r.state(t, "paused")
	r.c.InterceptPhrase(reqID, "Включи tame impala", false)
	r.c.OnReplyTrack(reqID, "The Less I Know The Better", "Tame Impala")
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, "spotify:artist:AAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatalf("Soloist получил %s", m)
	}
}

// Трека облака нет в первой выдаче: Spotify просят точнее (track:"…" artist:"…").
func TestCloudTrackFoundBySecondSearch(t *testing.T) {
	r, f := likeRig(t, nil)
	f.mu.Lock()
	f.searchFn = func(q, types string) string {
		if types == "track" && strings.Contains(q, `track:"Loser"`) && strings.Contains(q, `artist:"tame impala"`) {
			return `{"tracks":{"items":[{"uri":"spotify:track:TTTTTTTTTTTTTTTTTTTT09","name":"Loser","artists":[{"name":"Tame Impala"}]}]}}`
		}
		return `{"artists":{"items":[{"uri":"spotify:artist:AAAAAAAAAAAAAAAAAAAAAA","name":"Tame Impala"}]},"tracks":{"items":[]}}`
	}
	f.mu.Unlock()
	r.state(t, "paused")
	r.c.InterceptPhrase(reqID, "Включи лузер tame impala looser", false)
	r.c.OnReplyTrack(reqID, "Loser", "Tame Impala")
	r.c.OnReply(reqID, musicReply)
	if m := r.command(t); !strings.Contains(m, "spotify:track:TTTTTTTTTTTTTTTTTTTT09") {
		t.Fatalf("Soloist получил %s", m)
	}
}
