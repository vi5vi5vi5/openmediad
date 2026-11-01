package btout

import "openmediad/internal/libs/volmap"

// Таблица громкости платформы живёт в libs/volmap (её же использует spotify-soloist); здесь — прежние имена.

// QuasarConfig — настройки платформы, где лежит таблица громкости.
const QuasarConfig = volmap.QuasarConfig

// VolumeMap — таблица «уровень платформы → Master колонки».
type VolumeMap = volmap.Map

// DefaultVolumeMap — таблица bergamot (на случай, если quasar.cfg не прочитался).
func DefaultVolumeMap() VolumeMap { return volmap.Default() }

// ParseVolumeMap разбирает platformToAlsaMap — объект JSON в строке: {"уровень": Master, …}.
func ParseVolumeMap(s string) (VolumeMap, error) { return volmap.Parse(s) }

// LoadVolumeMap читает таблицу из quasar.cfg; нет файла или ключа — таблица bergamot.
func LoadVolumeMap(path string) (VolumeMap, error) { return volmap.Load(path) }
