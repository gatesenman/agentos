package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"strings"

	"github.com/agentos/agentos/internal/env"
)

// desktopContext is the build context of DesktopImage (Dockerfile +
// entrypoint), embedded so the server can build the image on first use.
//
//go:embed images/desktop
var desktopContext embed.FS

// desktopDebianContext is the build context of DesktopDebianImage.
//
//go:embed images/desktop-debian
var desktopDebianContext embed.FS

func contextTar(root embed.FS, subdir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	sub, err := fs.Sub(root, subdir)
	if err != nil {
		return nil, err
	}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: p, Mode: mode, Size: int64(len(b))}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func desktopContextTar() ([]byte, error) {
	return contextTar(desktopContext, "images/desktop")
}

func desktopDebianContextTar() ([]byte, error) {
	return contextTar(desktopDebianContext, "images/desktop-debian")
}

// buildDesktopImage builds DesktopImage from the embedded Ubuntu XFCE context.
func buildDesktopImage(ctx context.Context, cl *client) error {
	return buildEmbeddedDesktopImage(ctx, cl, DesktopImage, DesktopImageAlias, desktopContextTar)
}

// buildDesktopDebianImage builds DesktopDebianImage (Debian 13 + XFCE + Plank).
func buildDesktopDebianImage(ctx context.Context, cl *client) error {
	return buildEmbeddedDesktopImage(ctx, cl, DesktopDebianImage, DesktopDebianImageAlias, desktopDebianContextTar)
}

func buildEmbeddedDesktopImage(ctx context.Context, cl *client, image, alias string, tarFn func() ([]byte, error)) error {
	ctxTar, err := tarFn()
	if err != nil {
		return err
	}
	env.Logf(ctx, "info", "构建桌面镜像 / building desktop image %s (首次需几分钟 / first build takes a few minutes)", image)
	q := url.Values{"t": {image, alias}, "rm": {"1"}, "forcerm": {"1"}}
	resp, err := cl.do(ctx, "POST", "/build", q, bytes.NewReader(ctxTar), "application/x-tar")
	if err != nil {
		return fmt.Errorf("构建桌面镜像失败 / build %s: %w", image, err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("构建桌面镜像失败 / build %s: %w", image, err)
		}
		if m.Error != "" {
			return fmt.Errorf("构建桌面镜像失败 / build %s: %s", image, m.Error)
		}
		if s := strings.TrimSpace(m.Stream); strings.HasPrefix(s, "Step ") || strings.HasPrefix(s, "Successfully") {
			env.Logf(ctx, "info", "%s", s)
		}
	}
	if err := cl.json(ctx, "GET", "/images/"+image+"/json", nil, nil, nil); err != nil {
		return fmt.Errorf("构建桌面镜像失败 / build %s: image missing after build: %w", image, err)
	}
	env.Logf(ctx, "info", "桌面镜像就绪 / desktop image ready: %s", image)
	return nil
}
