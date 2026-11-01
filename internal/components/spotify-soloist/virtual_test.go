package spotifysoloist

import (
	"strings"
	"testing"
)

func TestVirtualPlayerFollowsSoloistState(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	v, _ := r.mach.virtual()
	if v == nil || !v.Playing || v.Track != "spotify:track:1" || v.Title != "Песня" || v.Artist != "Группа, Гость" || v.DurMs != 161123 {
		t.Fatalf("платформе сообщено %+v", v)
	}
	if v.PosMs != 82273 || v.PosAt.IsZero() {
		t.Fatalf("позиция %d, получена %v", v.PosMs, v.PosAt)
	}
	r.state(t, "paused")
	if v, _ = r.mach.virtual(); v == nil || v.Playing {
		t.Fatalf("после паузы сообщено %+v", v)
	}
	r.state(t, "idle")
	if v, _ = r.mach.virtual(); v != nil {
		t.Fatalf("после остановки виртуальный плеер остался: %+v", v)
	}
}

func TestVirtualPlayerIgnoresBuffering(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	_, before := r.mach.virtual()
	r.srv.push(t, `{"type":"playback_state","status":"buffering"}`)
	waitFor(t, "статус buffering", func() bool { return r.c.ctl.state().Status == "buffering" })
	v, after := r.mach.virtual()
	if after != before || v == nil || !v.Playing {
		t.Fatalf("буферизация изменила состояние: вызовов %d → %d, %+v", before, after, v)
	}
}

func TestVirtualPlayerSwitchedOffByConfig(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.VirtualPlayer = false })
	r.state(t, "playing")
	if v, _ := r.mach.virtual(); v != nil {
		t.Fatalf("virtual_player = false, а платформе сообщено %+v", v)
	}
}

func TestVirtualPlayerConfigReload(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.c.cancel = func() {} // как у запущенного компонента: иначе Reload только запоминает настройки
	n := r.c.config()
	n.VirtualPlayer = false
	r.c.Reload(&n)
	if v, _ := r.mach.virtual(); v != nil {
		t.Fatalf("после выключения на ходу плеер остался: %+v", v)
	}
	n.VirtualPlayer = true
	r.c.Reload(&n)
	if v, _ := r.mach.virtual(); v == nil || !v.Playing {
		t.Fatalf("после включения на ходу плеера нет: %+v", v)
	}
}

func TestPlatformCommandsReachSoloist(t *testing.T) {
	r := newVoiceRig(t, nil)
	r.state(t, "playing")
	r.mach.platformCommand("pause")
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("pause платформы стала %s", m)
	}
	r.mach.platformCommand("release")
	if m := r.command(t); !strings.Contains(m, `"command":"pause"`) {
		t.Fatalf("release платформы стал %s", m)
	}
	r.mach.platformCommand("play")
	if m := r.command(t); !strings.Contains(m, `"command":"play"`) || strings.Contains(m, "uri") {
		t.Fatalf("play платформы стал %s", m)
	}
	r.mach.platformCommand("other")
	r.noCommand(t)
}

func TestPlatformCommandsIgnoredWhenVirtualPlayerOff(t *testing.T) {
	r := newVoiceRig(t, func(c *Config) { c.VirtualPlayer = false })
	r.state(t, "playing")
	r.mach.platformCommand("pause")
	r.noCommand(t)
}
