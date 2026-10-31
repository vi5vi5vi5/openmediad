# scripts/dev — скрипты разработчика

| Файл | Что делает |
|---|---|
| `simulate.py` | симулятор колонки на компьютере: поддельные `mediad`, `platformd`, `aliced`, `iptables` и корень; проверяет `start / stop / install / страж` |
| `live.sh`, `shim-capture.sh`, `music-session.sh` | запись трафика шины с живой колонки (в `captures/`, в git не попадает) |
| `alice-say.sh`, `lat-series.sh` | текстовые вопросы Алисе и замер задержки |
| `volume-clamp.sh` | держит громкость колонки на заданном уровне во время проверок |
| `bt-test.sh` | проверка Bluetooth-наушников (**не запускался**: нужны реальные наушники) |
| `soloist-o3/` | опыты О1/О3 из `docs/MUSIC-PLAN.md`: Soloist на колонке без звука (вход аккаунта, память, кэш); см. `soloist-o3/README.md` |
