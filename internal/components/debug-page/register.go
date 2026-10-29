package debugpage

import (
	"context"
	"errors"

	"openmediad/internal/core/config"
	"openmediad/internal/core/journal"
	"openmediad/internal/core/registry"
)

// Config — секция [debug-page].
type Config struct {
	config.Toggle
	Listen string `conf:"listen"` // адрес порта: «0.0.0.0:6701» — для компьютеров домашней сети
	CORS   bool   `conf:"cors"`   // разрешить читать журнал странице с другого адреса (локальный html-файл)
}

const template = `[debug-page]             # отладочный веб-порт: живой журнал с пояснениями (только чтение)
enabled = true
listen = "0.0.0.0:6701"  # страница для компьютеров домашней сети: http://адрес-колонки:6701/
cors = false             # true — разрешить читать журнал странице с другого адреса (локальный html-файл)`

func init() {
	registry.Register(registry.Component{
		Name:     "debug-page",
		Defaults: func() any { return &Config{Toggle: config.Toggle{Enabled: true}, Listen: "0.0.0.0:6701"} },
		Template: template,
		New:      newModule,
	})
}

// module — отладочный порт как компонент демона.
type module struct {
	srv  *Server
	addr string
	logf func(string, ...any)
}

func newModule(env *registry.Env, cfg any) (any, error) {
	c := cfg.(*Config)
	j, ok := registry.Use[*journal.Journal](env, "journal")
	if !ok {
		return nil, errors.New("debug: нет журнала")
	}
	state, _ := registry.Use[func() any](env, "state")
	return &module{srv: &Server{Hub: j, CORS: c.CORS, State: state}, addr: c.Listen, logf: env.Logf("system")}, nil
}

// Start слушает порт в фоне (ошибка — в журнал: без отладочной страницы движок работает).
func (m *module) Start(ctx context.Context) error {
	go func() {
		if err := m.srv.Listen(ctx, m.addr); err != nil {
			m.logf("отладочный порт %s: %v", m.addr, err)
		}
	}()
	m.logf("отладочный порт: http://%s/ (только чтение)", m.addr)
	return nil
}

func (m *module) Stop() {}

// LogsDialog: отладочная страница просит писать в журнал реплики диалога с Алисой (распознанные фразы) — иначе их там нет.
func (m *module) LogsDialog() bool { return true }
