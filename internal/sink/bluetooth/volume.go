package bluetooth

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
)

// Громкость наушников в режиме speaker считается так же, как её считает сама платформа, иначе мы с ней спорим.
// Платформа хранит уровень 0–20 и переводит его в Master колонки по своей таблице (quasar.cfg:
// volume_capability.volume_manager_settings.platformToAlsaMap, у bergamot {"0": 0, "1": 33, "20": 255}, между точками —
// линейно). Громкость BlueALSA (0–127) она читает как громкость «Bluetooth-устройства» и ставит себе уровень
// round(громкость·20/127) («onVolumeChanged … source: bluetooth»). Поэтому наушникам ставится ровно round(уровень·127/20):
// обратный пересчёт даёт тот же уровень, и круг замыкается без сдвига. Линейный пересчёт Master → 0–127 сдвигал уровень
// на единицу при каждом обороте, и громкость сама уезжала на 14 из 20 (70 %) [проверено 28.10.2026, BUS-PROTOCOL.md §2.7б].

// QuasarConfig — настройки платформы, где лежит таблица громкости.
const QuasarConfig = "/system/vendor/quasar/quasar.cfg"

// VolumeMap — таблица «уровень платформы → Master колонки».
type VolumeMap struct {
	points [][2]int // (уровень, Master) по возрастанию уровня; последний уровень — верх шкалы
}

// DefaultVolumeMap — таблица bergamot (на случай, если quasar.cfg не прочитался).
func DefaultVolumeMap() VolumeMap {
	return VolumeMap{points: [][2]int{{0, 0}, {1, 33}, {20, 255}}}
}

// ParseVolumeMap разбирает platformToAlsaMap — объект JSON в строке: {"уровень": Master, …}.
func ParseVolumeMap(s string) (VolumeMap, error) {
	var raw map[string]int
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return VolumeMap{}, fmt.Errorf("platformToAlsaMap: %w", err)
	}
	var m VolumeMap
	for k, v := range raw {
		lvl, err := strconv.Atoi(k)
		if err != nil || lvl < 0 {
			return VolumeMap{}, fmt.Errorf("platformToAlsaMap: уровень %q", k)
		}
		m.points = append(m.points, [2]int{lvl, v})
	}
	sort.Slice(m.points, func(i, j int) bool { return m.points[i][0] < m.points[j][0] })
	if len(m.points) < 2 || m.points[0][0] != 0 {
		return VolumeMap{}, errors.New("platformToAlsaMap: нужны хотя бы уровень 0 и верхний")
	}
	return m, nil
}

// LoadVolumeMap читает таблицу из quasar.cfg; нет файла или ключа — таблица bergamot.
func LoadVolumeMap(path string) (VolumeMap, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return DefaultVolumeMap(), err
	}
	var cfg struct {
		VolumeCapability struct {
			Settings struct {
				Map string `json:"platformToAlsaMap"`
			} `json:"volume_manager_settings"`
		} `json:"volume_capability"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DefaultVolumeMap(), fmt.Errorf("%s: %w", path, err)
	}
	if cfg.VolumeCapability.Settings.Map == "" {
		return DefaultVolumeMap(), fmt.Errorf("%s: нет volume_capability.volume_manager_settings.platformToAlsaMap", path)
	}
	m, err := ParseVolumeMap(cfg.VolumeCapability.Settings.Map)
	if err != nil {
		return DefaultVolumeMap(), err
	}
	return m, nil
}

func (m VolumeMap) table() [][2]int {
	if len(m.points) == 0 {
		return DefaultVolumeMap().points
	}
	return m.points
}

// Levels — верх шкалы платформы (20).
func (m VolumeMap) Levels() int {
	p := m.table()
	return p[len(p)-1][0]
}

// Master — значение Master колонки для уровня платформы.
func (m VolumeMap) Master(level int) int {
	p := m.table()
	if level <= p[0][0] {
		return p[0][1]
	}
	for i := 1; i < len(p); i++ {
		if level <= p[i][0] {
			a, b := p[i-1], p[i]
			return int(math.Round(float64(a[1]) + float64(level-a[0])*float64(b[1]-a[1])/float64(b[0]-a[0])))
		}
	}
	return p[len(p)-1][1]
}

// Level — уровень платформы, которому соответствует Master колонки (ближайший).
func (m VolumeMap) Level(master int) int {
	best, bestD := 0, math.MaxInt
	for l := 0; l <= m.Levels(); l++ {
		d := m.Master(l) - master
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = l, d
		}
	}
	return best
}

// Headphones — громкость BlueALSA (0–127) для Master колонки: так же, как платформа ставит её телефону.
func (m VolumeMap) Headphones(master int) byte {
	return byte(math.Round(float64(m.Level(master)) * 127 / float64(m.Levels())))
}
