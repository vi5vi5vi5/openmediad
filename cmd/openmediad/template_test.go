package main

import (
	"reflect"
	"testing"

	"openmediad/internal/config"
)

// Файл настроек по умолчанию (его пишут install и config reset) собирается из шаблонов ядра и компонентов этой сборки:
// он обязан разбираться и давать ровно умолчания. Шаблоны компонентов проверяются только здесь — в пакете config
// компоненты не подключены.
func TestDefaultTemplateLoadsToDefaults(t *testing.T) {
	c, err := config.Load(config.Template())
	if err != nil {
		t.Fatalf("шаблон настроек не разбирается: %v", err)
	}
	if len(c.Warnings) > 0 {
		t.Fatalf("предупреждения в шаблоне: %v", c.Warnings)
	}
	if def := config.Defaults(); !reflect.DeepEqual(c.Components, def.Components) {
		for name, v := range def.Components {
			if !reflect.DeepEqual(c.Components[name], v) {
				t.Errorf("[%s]: в шаблоне %+v, умолчания %+v", name, c.Components[name], v)
			}
		}
	}
}
