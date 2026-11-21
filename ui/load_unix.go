//go:build !windows

package ui

import (
	"runtime"

	"github.com/ebitengine/purego"
)

var libName = func() string {
	if runtime.GOOS == "darwin" {
		return "libquevu_ui.dylib"
	}
	return "libquevu_ui.so"
}()

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
