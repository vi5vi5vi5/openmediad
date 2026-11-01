package spotifysoloist

import (
	"context"
	"errors"
	"slices"
	"time"
)

// «Алиса, включи <исполнитель, песня, альбом, жанр>» из Spotify.
//
// Время идёт так (от конца фразы, T): в T демон отдаёт нам текст фразы; примерно в T+1 с приходит ответ облака, а если это музыка — ещё через
// 0,1–2 с команда запуска трека Яндекса (после голоса «Включаю»). Поэтому:
//
//  1. В T разбираем фразу (playreq.go) и начинаем искать: сначала среди своих названий (aliases.go), потом в Spotify (search.go, если есть вход и доступ к Web API).
//  2. Облако понимает фразу лучше нас (мы видим только слова распознавателя: «looser tame impala»), и по его ответу видно, какой трек оно выбрало
//     (audio_play.metadata: «Loser» / «Tame Impala»). Если название этого трека слышно в нашем запросе, просили именно трек, и мы включаем его (а не «просто Tame Impala»);
//     если нет («включи Tame Impala» — облако выбрало случайный трек исполнителя) — включаем найденное по запросу.
//  3. «Голая» просьба («включи Кино») могла быть и про свет, и про фильм. Её выполняем только после вердикта облака: оно отвечает на музыку директивой audio_play.
//     Явную («включи песню …», «включи группу …») выполняем и без вердикта, если он не пришёл за explicitWait (облако могло не знать такую музыку).
//
// Если облако ответило «Включаю.», запуск Яндекса, который идёт следом, достаётся Spotify (ArmCarrier), а её голос гасится, если не выключено quiet_reply. Если облако не ответило, диалог
// отменяется и голос гасится.
// Ничего не найдено, облако считает просьбу не музыкой или Spotify недоступен — мы ничего не делаем, и Алиса отвечает как обычно.

const (
	requestTTL      = 30 * time.Second // сколько помним просьбу, чьего ответа ждём
	resolveTimeout  = 8 * time.Second  // сколько ждём поиска
	explicitWait    = 2 * time.Second  // сколько явная просьба ждёт вердикт облака
	playArmWindow   = 8 * time.Second  // сколько после решения запуск Яндекса от облака достаётся Spotify: он идёт после голоса Алисы
	maxPendingReqs  = 8
	searchPerKind   = 5
	searchHintEvery = 10 * time.Minute
)

// verdict — что облако думает о просьбе.
type verdict int

const (
	verdictUnknown verdict = iota
	verdictMusic
	verdictOther
)

// resolution — результат поиска того, что включить.
type resolution struct {
	URI, Name, How string
	Res            *searchResult // выдача Spotify (для уточнения по ответу облака); nil для своих названий
}

type cloudTrack struct{ Title, Artist string }

type pendingReq struct {
	id    string
	req   playRequest
	at    time.Time
	done  bool // поиск закончен
	res   resolution
	v     verdict
	cloud *cloudTrack // трек, который облако собралось включить
	acted bool
}

// beginPlayRequest запоминает просьбу и ищет в фоне. Вызывается из цикла чтения диалога Алисы: не блокирует.
func (c *Component) beginPlayRequest(reqID string, r playRequest) {
	c.mu.Lock()
	if c.reqs == nil {
		c.reqs = map[string]*pendingReq{}
	}
	now := time.Now()
	for id, p := range c.reqs { // забыть то, чего не дождались
		if now.Sub(p.at) > requestTTL {
			delete(c.reqs, id)
		}
	}
	if len(c.reqs) >= maxPendingReqs {
		c.mu.Unlock()
		return
	}
	p := &pendingReq{id: reqID, req: r, at: now}
	c.reqs[reqID] = p
	c.mu.Unlock()
	go func() {
		res, err := c.resolveName(r)
		c.mu.Lock()
		p.done, p.res = true, res
		c.mu.Unlock()
		if err != nil {
			c.hintSearch(err)
		}
		c.maybePlay(p)
	}()
	if r.Explicit { // вердикт облака для явной просьбы не обязателен: ждём его недолго
		time.AfterFunc(explicitWait, func() { c.maybePlay(p) })
	}
}

// resolveName ищет, что включить: своё название, потом поиск в Spotify. Тест подменяет c.resolve.
func (c *Component) resolveName(r playRequest) (resolution, error) {
	if c.resolve != nil {
		return c.resolve(r)
	}
	if uri, ok := c.aliases.lookup(r.Query, r.Whole); ok {
		return resolution{URI: uri, How: "alias"}, nil
	}
	if c.web == nil {
		return resolution{}, errors.New("поиск недоступен")
	}
	if err := c.web.ready(); err != nil {
		return resolution{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	res, err := c.web.search(ctx, r.Query, searchPerKind)
	if err != nil {
		return resolution{}, err
	}
	hit, ok := pick(r.Kind, r.Query, res)
	if !ok {
		return resolution{}, nil
	}
	return resolution{URI: hit.URI, Name: hit.Name, How: "search", Res: &res}, nil
}

// hintSearch пишет в журнал, почему поиск не сработал (не чаще раза в 10 минут: просьб «включи …» много, а причина одна).
func (c *Component) hintSearch(err error) {
	c.mu.Lock()
	show := time.Since(c.searchHintAt) > searchHintEvery
	if show {
		c.searchHintAt = time.Now()
	}
	c.mu.Unlock()
	if show {
		c.logf("«включи <название>»: поиск в Spotify не вышел: %v. Свои названия работают всегда: openmediad spotify alias", err)
	}
}

// OnReplyTrack получает трек, который облако собралось включить (интерфейс демона trackHandler); приходит раньше OnReply того же ответа.
func (c *Component) OnReplyTrack(reqID, title, artist string) {
	c.mu.Lock()
	if p := c.reqs[reqID]; p != nil && p.cloud == nil {
		p.cloud = &cloudTrack{Title: title, Artist: artist}
	}
	c.mu.Unlock()
}

// OnReply получает ответ облака на запрос целиком (интерфейс демона replyHandler): по директивам видно, считает ли оно просьбу музыкой.
func (c *Component) OnReply(reqID string, directives []string) {
	c.mu.Lock()
	p := c.reqs[reqID]
	if p == nil || p.v != verdictUnknown {
		c.mu.Unlock()
		return
	}
	if slices.Contains(directives, "audio_play") {
		p.v = verdictMusic
	} else {
		p.v = verdictOther
	}
	drop := p.v == verdictOther && !p.req.Explicit
	if drop {
		delete(c.reqs, reqID)
	}
	c.mu.Unlock()
	if !drop {
		c.maybePlay(p)
	}
}

// maybePlay решает, пора ли запускать: есть результат поиска и (для «голой» просьбы) вердикт облака «это музыка», а для явной — вердикт или истёк explicitWait.
func (c *Component) maybePlay(p *pendingReq) {
	c.mu.Lock()
	if p.acted || !p.done {
		c.mu.Unlock()
		return
	}
	if p.res.URI == "" { // не нашли
		delete(c.reqs, p.id)
		c.mu.Unlock()
		return
	}
	var ready bool
	if p.req.Explicit {
		ready = p.v != verdictUnknown || time.Since(p.at) >= explicitWait-20*time.Millisecond
	} else {
		ready = p.v == verdictMusic
	}
	if !ready {
		c.mu.Unlock()
		return // «голая» просьба ждёт вердикт; если облако скажет «не музыка», просьба будет забыта в OnReply
	}
	p.acted = true
	delete(c.reqs, p.id)
	v, cloud, res, query := p.v, p.cloud, p.res, p.req.Query
	c.mu.Unlock()

	if c.ctl == nil {
		return
	}
	if ps := c.ctl.state(); !ps.Connected || !ps.LoggedIn {
		c.logf("«включи <название>»: Spotify не готов (Soloist не подключён или не вошёл в аккаунт)")
		return
	}
	// Что делать с Алисой, решаем сразу (это быстро), а выбор трека и запуск могут занять время на сеть.
	if v != verdictOther && c.mach != nil {
		c.mach.ArmCarrier(playArmWindow) // запуск Яндекса от облака, если он придёт, достанется Spotify
	}
	switch v {
	case verdictMusic: // облако ответило «Включаю …»: по желанию пользователя её голос не нужен (запрос выполняет Spotify)
		if c.config().QuietReply && c.swallow != nil {
			c.swallow()
		}
	case verdictUnknown: // облако не ответило: диалог не нужен, а если успеет ответить — голос «не нашла» гасим
		if c.config().VoiceCancel {
			go c.cancelDialog()
		}
		if c.swallow != nil {
			c.swallow()
		}
	case verdictOther: // облако не поняло или не нашло: пусть не говорит, играть будет Spotify
		if c.swallow != nil {
			c.swallow()
		}
	}
	go c.startByName(res, cloud, query)
}

// startByName выбирает трек (уточняя по облаку) и запускает его в Soloist.
func (c *Component) startByName(res resolution, cloud *cloudTrack, query string) {
	uri, name, how := res.URI, res.Name, res.How
	if res.How != "alias" && cloud != nil {
		if titleInQuery(words(query), cloud.Title) {
			if hit, ok := c.cloudTrackHit(res.Res, cloud); ok {
				uri, name, how = hit.URI, hit.Name, "search+облако"
			} else {
				c.logf("облако выбрало «%s — %s», в Spotify такого трека не нашлось: включаю найденное по запросу", cloud.Artist, cloud.Title)
			}
		} else {
			c.logf("облако выбрало «%s — %s», но этого названия в запросе нет (просили исполнителя или жанр): включаю найденное по запросу", cloud.Artist, cloud.Title)
		}
	}
	if err := c.ctl.PlayURI(uri); err != nil {
		c.logf("«включи <название>»: Soloist не принял команду: %v", err)
		return
	}
	c.mu.Lock()
	c.voiceHandled++
	c.voiceLast = "включить по названию"
	c.voiceAt = time.Now()
	c.mu.Unlock()
	if name != "" {
		c.logf("включено по названию (%s): %s", how, name)
	} else {
		c.logf("включено по названию (%s)", how)
	}
}

// cloudTrackHit находит в Spotify трек, который назвало облако: сначала в уже полученной выдаче, потом точным поиском по названию и исполнителю.
func (c *Component) cloudTrackHit(res *searchResult, cloud *cloudTrack) (searchHit, bool) {
	if res != nil {
		if h, ok := findTrack(res.Tracks, cloud.Title, cloud.Artist); ok {
			return h, true
		}
	}
	if c.web == nil || c.web.ready() != nil {
		return searchHit{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	r, err := c.web.searchTrack(ctx, cleanTitle(cloud.Title), firstArtist(cloud.Artist), searchPerKind)
	if err != nil {
		return searchHit{}, false
	}
	return findTrack(r.Tracks, cloud.Title, cloud.Artist)
}
