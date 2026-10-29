//go:build !(linux && (arm64 || amd64))

package alsactl

import "errors"

func openControl(int) (control, error) {
	return nil, errors.New("alsactl: регуляторы ALSA есть только на колонке (64-битный Linux)")
}
