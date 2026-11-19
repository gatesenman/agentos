//go:build windows

package virtual

import "errors"

// Desktop is unavailable on Windows (the node uses the built-in screen stream).
type Desktop struct{ Display, VNCAddr string }

func Start(Options) (*Desktop, error) {
	return nil, errors.New("virtual desktop is not supported on Windows (the built-in screen stream is used)")
}
func (d *Desktop) Env() []string { return nil }
func (d *Desktop) Stop()         {}
