package ui

import "syscall"

const libName = "quevu_ui.dll"

func openLibrary(path string) (uintptr, error) {
	h, err := syscall.LoadLibrary(path)
	return uintptr(h), err
}
