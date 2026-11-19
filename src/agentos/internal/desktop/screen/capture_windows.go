//go:build windows

package screen

import (
	"errors"
	"image"

	"github.com/kbinani/screenshot"
)

// screenshotCapturer uses github.com/kbinani/screenshot (GDI BitBlt, pure Go
// on windows). Bounds are physical pixels in virtual-desktop coordinates.
type screenshotCapturer struct{}

func newPlatformCapturer() (Capturer, error) {
	if !platformAvailable() {
		return nil, errors.New(unavailableReason())
	}
	return screenshotCapturer{}, nil
}

func (screenshotCapturer) Displays() ([]image.Rectangle, error) {
	n := screenshot.NumActiveDisplays()
	out := make([]image.Rectangle, 0, n)
	for i := 0; i < n; i++ {
		if b := screenshot.GetDisplayBounds(i); !b.Empty() {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		if b, ok := fallbackBounds(); ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func (screenshotCapturer) Capture(r image.Rectangle) (img *image.RGBA, err error) {
	defer func() { // the library may panic on X protocol errors
		if p := recover(); p != nil {
			img, err = nil, errors.New("capture panic")
		}
	}()
	return screenshot.CaptureRect(r)
}

func (screenshotCapturer) Close() error { return nil }
