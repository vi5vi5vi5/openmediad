# platform/layout — где что лежит

```
/data/opt/openmediad/        всё своё на постоянном разделе: bin/, guard.sh, openmediad.conf, backup/, state/
/etc/init.d/S98openmediad    скрипт автозапуска — единственный наш файл в rootfs
/tmp/openmediad/             ctl.sock, pid, журнал стража (tmpfs = ОЗУ: только маленькие файлы)
```

`Root` берётся из `OPENMEDIAD_ROOT` (так симулятор колонки и тесты работают во временном каталоге).
