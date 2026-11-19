//go:build !windows && !linux

package screen

// Unsupported platforms (darwin needs cgo for capture): Available()=false.

func platformAvailable() bool { return false }

func newPlatformCapturer() (Capturer, error) { return nil, ErrNotImplemented }

func newPlatformInjector() (Injector, error) { return nil, ErrNotImplemented }
