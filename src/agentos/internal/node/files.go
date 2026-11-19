package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/agentos/agentos/internal/api"
	"github.com/gorilla/websocket"
)

const fileChunk = 64 * 1024

// home is the default working directory for exec and the base for relative
// file paths (<node-data>/home).
func (n *Node) home() string { return filepath.Join(n.cfg.DataDir, "home") }

func (n *Node) resolvePath(p string) string {
	if p == "" {
		return n.home()
	}
	if filepath.IsAbs(p) || (len(p) > 0 && (p[0] == '/' || p[0] == '\\')) {
		return filepath.Clean(p)
	}
	return filepath.Join(n.home(), p)
}

type tunnelFunc func(ctx context.Context, c *websocket.Conn, params map[string]string) error

func (n *Node) builtinTunnel(kind string) tunnelFunc {
	switch kind {
	case api.TunnelExec:
		return n.serveExec
	case api.TunnelFileGet:
		return n.serveFileGet
	case api.TunnelFilePut:
		return n.serveFilePut
	}
	return nil
}

func textMsg(c *websocket.Conn, v any) error {
	b, _ := json.Marshal(v)
	return c.WriteMessage(websocket.TextMessage, b)
}

func sendErr(c *websocket.Conn, err error) error {
	textMsg(c, map[string]string{"type": "error", "message": err.Error()})
	return err
}

func (n *Node) listDir(p string) (api.FsListResponse, error) {
	abs := n.resolvePath(p)
	ents, err := os.ReadDir(abs)
	if err != nil {
		return api.FsListResponse{}, err
	}
	out := api.FsListResponse{Path: abs, Entries: make([]api.FsEntry, 0, len(ents))}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, api.FsEntry{Name: e.Name(), Size: fi.Size(), Dir: fi.IsDir(),
			Mode: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UnixMilli()})
	}
	return out, nil
}

func (n *Node) serveFileGet(ctx context.Context, c *websocket.Conn, params map[string]string) error {
	f, err := os.Open(n.resolvePath(params["path"]))
	if err != nil {
		return sendErr(c, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return sendErr(c, err)
	}
	if fi.IsDir() {
		return sendErr(c, errors.New("is a directory"))
	}
	if err := textMsg(c, map[string]any{"type": "info", "size": fi.Size(), "mode": uint32(fi.Mode().Perm())}); err != nil {
		return err
	}
	buf := make([]byte, fileChunk)
	for {
		k, rerr := f.Read(buf)
		if k > 0 {
			if err := c.WriteMessage(websocket.BinaryMessage, buf[:k]); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return sendErr(c, rerr)
		}
	}
	textMsg(c, map[string]string{"type": "eof"})
	c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline())
	return nil
}

func (n *Node) serveFilePut(ctx context.Context, c *websocket.Conn, params map[string]string) error {
	dst := n.resolvePath(params["path"])
	mode := os.FileMode(0o644)
	if m, err := strconv.ParseUint(params["mode"], 8, 32); err == nil && m > 0 {
		mode = os.FileMode(m)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return sendErr(c, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".agentos-upload-*")
	if err != nil {
		return sendErr(c, err)
	}
	defer os.Remove(tmp.Name())
	var size int64
	for {
		t, data, err := c.ReadMessage()
		if err != nil {
			tmp.Close()
			return err // aborted upload: temp file removed
		}
		if t == websocket.BinaryMessage {
			k, err := tmp.Write(data)
			size += int64(k)
			if err != nil {
				tmp.Close()
				return sendErr(c, err)
			}
			continue
		}
		var m struct{ Type string }
		json.Unmarshal(data, &m)
		if m.Type == "eof" {
			break
		}
	}
	if err := tmp.Close(); err != nil {
		return sendErr(c, err)
	}
	os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		// Windows cannot rename over an existing file opened elsewhere; retry after remove.
		os.Remove(dst)
		if err := os.Rename(tmp.Name(), dst); err != nil {
			return sendErr(c, fmt.Errorf("save %s: %w", dst, err))
		}
	}
	return textMsg(c, map[string]any{"type": "ok", "size": size})
}
