package spotifysoloist

import (
	"context"
	"time"
)

// Часы колонки при включении показывают 1970 год, а правильное время появляется, когда поднимется сеть (при загрузке это минуты, а бывает и часы).
// Soloist стартует раньше: сертификаты Spotify для него «ещё недействительны», он отвечает «command requires authentication» и больше не пробует.
// Колонка так и висит в «ждёт входа», пока кто-нибудь не перезапустит Soloist (после перезагрузки так и было). Поэтому, когда часы скакнули вперёд, а входа
// в Spotify всё ещё нет, Soloist перезапускается: теперь он входит сам, по сохранённому входу.

// clockJump — на сколько часы должны уйти вперёд сверх прошедшего времени, чтобы это считалось выправлением (сетевая синхронизация), а не дрейфом.
const clockJump = 10 * time.Minute

// clockSource отдаёт часы стены (с настройкой по сети они прыгают) и прошедшее время по монотонным часам (не прыгают).
type clockSource func() (wall time.Time, elapsed time.Duration)

// watchClock раз в every сравнивает ход часов стены с монотонным. Скачок вперёд больше jump: ждёт settle (сеть только что поднялась) и,
// если входа всё ещё нет, просит перезапустить Soloist. Идёт до отмены ctx.
func watchClock(ctx context.Context, every, settle, jump time.Duration, src clockSource, loggedIn func() bool, restart func(now time.Time)) {
	lastWall, lastElapsed := src()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		wall, elapsed := src()
		skipped := wall.Sub(lastWall) - (elapsed - lastElapsed)
		lastWall, lastElapsed = wall, elapsed
		if skipped < jump {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(settle):
		}
		if !loggedIn() {
			restart(wall)
		}
		lastWall, lastElapsed = src() // время ожидания в скачок не считается
	}
}

// clockWatch следит за выправлением часов колонки (см. выше).
func (c *Component) clockWatch(ctx context.Context) {
	start := time.Now()
	src := func() (time.Time, time.Duration) { return time.Now().Round(0), time.Since(start) }
	watchClock(ctx, 5*time.Second, 5*time.Second, clockJump, src,
		func() bool { return c.ctl.state().LoggedIn },
		func(now time.Time) {
			c.logf("часы колонки выправились (теперь %s), а входа в Spotify нет: перезапускаю Soloist (со старыми часами при загрузке он войти не мог)", now.Format("2006-01-02 15:04"))
			c.sup.restart()
		})
}
