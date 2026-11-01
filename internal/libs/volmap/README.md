# volmap — таблица громкости платформы

Платформа хранит громкость уровнем 0–20 и переводит его в регулятор Master колонки по своей таблице (`quasar.cfg`:
`volume_capability.volume_manager_settings.platformToAlsaMap`, у bergamot `{"0": 0, "1": 33, "20": 255}`, между точками линейно). Чтобы не спорить с
платформой, всё, что связано с громкостью (наушники, ползунок Spotify), считает так же, как она.

| Вызов | Что делает |
|---|---|
| `Load(path)`, `Parse(s)`, `Default()` | таблица из `quasar.cfg`, из строки JSON, таблица bergamot |
| `Master(level)` | значение Master для уровня платформы |
| `Level(master)` | ближайший уровень платформы для значения Master |
| `Levels()` | верх шкалы (20) |
| `Headphones(master)` | громкость BlueALSA 0–127 для Master (так же, как платформа ставит её наушникам) |

**Кому нужна:** `bt-out`, `spotify-soloist`.
