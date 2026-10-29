//go:build !unix

package disk

import "errors"

// FreeKB на этой системе не поддерживается.
func FreeKB(path string) (int64, error) { return 0, errors.New("не поддерживается") }
