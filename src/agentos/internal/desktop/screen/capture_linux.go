//go:build linux

package screen

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"sync"
	"unsafe"

	"github.com/jezek/xgb"
	mshm "github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xinerama"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"
)

func init() {
	// xgb logs harmless notices (e.g. missing ~/.Xauthority) to stderr.
	xgb.Logger = log.New(io.Discard, "", 0)
}

// xCapturer grabs the X11 root window over one persistent connection, using
// a reusable MIT-SHM segment when available (falls back to GetImage).
type xCapturer struct {
	mu     sync.Mutex
	c      *xgb.Conn
	root   xproto.Window
	screen image.Rectangle
	xin    bool

	useShm  bool
	seg     mshm.Seg
	shmID   int
	shmData []byte
}

func newPlatformCapturer() (Capturer, error) {
	c, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", unavailableReason(), err)
	}
	s := xproto.Setup(c).DefaultScreen(c)
	if s.RootDepth != 24 && s.RootDepth != 32 {
		c.Close()
		return nil, fmt.Errorf("unsupported X root depth %d (need 24/32)", s.RootDepth)
	}
	x := &xCapturer{c: c, root: s.Root, screen: image.Rect(0, 0, int(s.WidthInPixels), int(s.HeightInPixels)), shmID: -1}
	x.xin = xinerama.Init(c) == nil
	x.useShm = mshm.Init(c) == nil
	return x, nil
}

func (x *xCapturer) Displays() ([]image.Rectangle, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return nil, errors.New("closed")
	}
	if x.xin {
		if r, err := xinerama.QueryScreens(x.c).Reply(); err == nil && len(r.ScreenInfo) > 0 {
			out := make([]image.Rectangle, 0, len(r.ScreenInfo))
			for _, si := range r.ScreenInfo {
				b := image.Rect(int(si.XOrg), int(si.YOrg), int(si.XOrg)+int(si.Width), int(si.YOrg)+int(si.Height))
				if !b.Empty() {
					out = append(out, b)
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	return []image.Rectangle{x.screen}, nil
}

func (x *xCapturer) ensureShm(size int) error {
	if x.shmData != nil && len(x.shmData) >= size {
		return nil
	}
	x.releaseShm()
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0600)
	if err != nil {
		return err
	}
	data, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return err
	}
	seg, err := mshm.NewSegId(x.c)
	if err == nil {
		err = mshm.AttachChecked(x.c, seg, uint32(id), false).Check()
	}
	// Mark for deletion now: freed automatically once both sides detach/exit.
	unix.SysvShmCtl(id, unix.IPC_RMID, nil)
	if err != nil {
		unix.SysvShmDetach(data)
		return err
	}
	x.seg, x.shmID, x.shmData = seg, id, data
	return nil
}

func (x *xCapturer) releaseShm() {
	if x.shmData == nil {
		return
	}
	mshm.Detach(x.c, x.seg)
	unix.SysvShmDetach(x.shmData)
	x.shmData, x.shmID = nil, -1
}

func (x *xCapturer) Capture(r image.Rectangle) (*image.RGBA, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return nil, errors.New("closed")
	}
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	in := r.Intersect(x.screen)
	if in.Empty() {
		return img, nil
	}
	w, h := in.Dx(), in.Dy()
	var data []byte
	if x.useShm {
		if err := x.ensureShm(w * h * 4); err != nil {
			x.useShm = false // e.g. remote X server: fall back to GetImage
		} else if _, err := mshm.GetImage(x.c, xproto.Drawable(x.root), int16(in.Min.X), int16(in.Min.Y),
			uint16(w), uint16(h), 0xffffffff, xproto.ImageFormatZPixmap, x.seg, 0).Reply(); err != nil {
			x.useShm = false // e.g. server cannot access our segment: fall back to GetImage
			x.releaseShm()
		} else {
			data = x.shmData[:w*h*4]
		}
	}
	if data == nil {
		// Core GetImage is limited by the max request length; fetch in strips.
		data = make([]byte, w*h*4)
		strip := (4 << 20) / (w * 4)
		if strip < 1 {
			strip = 1
		}
		for y := 0; y < h; y += strip {
			sh := strip
			if y+sh > h {
				sh = h - y
			}
			rep, err := xproto.GetImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(x.root),
				int16(in.Min.X), int16(in.Min.Y+y), uint16(w), uint16(sh), 0xffffffff).Reply()
			if err != nil {
				return nil, fmt.Errorf("GetImage: %w", err)
			}
			copy(data[y*w*4:], rep.Data)
		}
	}
	// BGRX (little-endian 32bpp ZPixmap) → RGBA, 4 bytes at a time.
	ox, oy := in.Min.X-r.Min.X, in.Min.Y-r.Min.Y
	for y := 0; y < h; y++ {
		src := data[y*w*4 : (y+1)*w*4]
		dst := img.Pix[(oy+y)*img.Stride+ox*4 : (oy+y)*img.Stride+(ox+w)*4]
		s32 := unsafe.Slice((*uint32)(unsafe.Pointer(&src[0])), w)
		d32 := unsafe.Slice((*uint32)(unsafe.Pointer(&dst[0])), w)
		for i, v := range s32 {
			d32[i] = (v&0xff)<<16 | (v & 0xff00) | (v>>16)&0xff | 0xff000000
		}
	}
	return img, nil
}

func (x *xCapturer) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.c == nil {
		return nil
	}
	x.releaseShm()
	x.c.Close()
	x.c = nil
	return nil
}
