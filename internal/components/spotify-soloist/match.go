package spotifysoloist

import (
	"strings"
	"unicode"
)

// Нечёткое сравнение названий. Фразу слышит распознаватель речи, названия лежат в каталоге Spotify: «looser» и «Loser», «ё» и «е», знаки и скобки
// («Loser (Remastered 2012)») не должны мешать узнать трек.

// words режет текст на слова: строчные буквы, «е» вместо «ё», без знаков. В отличие от normalize не выбрасывает служебные слова
// («давай», «слушай» бывают частью названия песни).
func words(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// fuzzyEq — слова совпадают или отличаются на пару опечаток (для слов от 4 букв).
func fuzzyEq(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	short := min(len(ra), len(rb))
	if short < 4 || abs(len(ra)-len(rb)) > 2 {
		return false
	}
	edits := 1
	if short >= 7 {
		edits = 2
	}
	return levenshtein(ra, rb) <= edits
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func anyFuzzy(w string, set []string) bool {
	for _, s := range set {
		if fuzzyEq(w, s) {
			return true
		}
	}
	return false
}

// coverage — сколько слов запроса q нашлось среди слов кандидата и сколько слов кандидата лишние (в запросе их нет).
func coverage(q, cand []string) (covered, extra int) {
	for _, w := range q {
		if anyFuzzy(w, cand) {
			covered++
		}
	}
	for _, c := range cand {
		if !anyFuzzy(c, q) {
			extra++
		}
	}
	return covered, extra
}

// matchScore — насколько кандидат подходит под запрос: доля покрытых слов запроса минус небольшой штраф за лишние слова кандидата, плюс бонус за точное название.
// «looser tame impala»: трек «Loser» (Tame Impala) покрывает все три слова, исполнитель «Tame Impala» — два, поэтому выигрывает трек;
// «tame impala»: исполнитель покрывает всё без лишнего, трек «Loser» (Tame Impala) — с одним лишним словом, поэтому выигрывает исполнитель.
func matchScore(q []string, name string, artists ...string) float64 {
	if len(q) == 0 {
		return 0
	}
	nameWords := words(name)
	cand := append([]string(nil), nameWords...)
	for _, a := range artists {
		cand = append(cand, words(a)...)
	}
	covered, extra := coverage(q, cand)
	score := float64(covered)/float64(len(q)) - 0.08*float64(extra)
	if len(nameWords) == len(q) {
		if c, e := coverage(q, nameWords); c == len(q) && e == 0 {
			score += 0.05 // название совпало с запросом целиком
		}
	}
	return score
}

// cleanTitle убирает из названия трека пояснения в скобках и после тире/«feat.» («Loser (Remastered 2012)» → «Loser»).
func cleanTitle(s string) string {
	for _, cut := range []string{" (", " [", " - ", " – ", " — ", " feat", " ft.", " with "} {
		if i := strings.Index(strings.ToLower(s), cut); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// firstArtist — первый исполнитель из подписи «A, B & C feat. D».
func firstArtist(s string) string {
	s = strings.ToLower(s)
	for _, sep := range []string{",", " & ", " feat", " ft.", " и ", " x ", ";"} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// titleInQuery — все слова названия трека слышны в запросе пользователя (с поправкой на опечатки): значит, он просил именно этот трек, а не исполнителя вообще.
func titleInQuery(q []string, title string) bool {
	tw := words(cleanTitle(title))
	if len(tw) == 0 || len(strings.Join(tw, "")) < 3 {
		return false
	}
	for _, w := range tw {
		if !anyFuzzy(w, q) {
			return false
		}
	}
	return true
}
