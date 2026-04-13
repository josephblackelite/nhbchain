package common

import "errors"

var ErrModulePaused = errors.New("module paused")

type PauseView interface {
	IsPaused(module string) bool
}

func Guard(p PauseView, module string) error {
	if p == nil || module == "" {
		return nil
	}
	if p.IsPaused(module) {
		return ErrModulePaused
	}
	return nil
}
