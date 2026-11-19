package hyperv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentos/agentos/internal/env"
)

// resolveISO returns a local install ISO. With iso_path empty it downloads the
// official image into <store>/iso (resumable, SHA-256 verified when the
// checksum is published) and reuses it for later VMs.
func resolveISO(ctx context.Context, c *Config) (path string, needsManual bool, note string, err error) {
	if c.ISOPath != "" {
		if st, e := os.Stat(c.ISOPath); e != nil || st.IsDir() {
			return "", false, "", fmt.Errorf("hyperv: ISO 路径无效: %s", c.ISOPath)
		}
		return c.ISOPath, false, "", nil
	}
	if c.ISOURL == "" {
		return "", true, "自定义客户机需提供 iso_path", nil
	}
	name := isoNameFromURL(c.ISOURL)
	if name == "" || name == "." || name == "/" {
		name = c.GuestOS + ".iso"
	}
	dir := filepath.Join(c.StoreRoot, "iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, "", fmt.Errorf("hyperv: 创建 ISO 缓存目录失败: %w", err)
	}
	dest := filepath.Join(dir, name)
	sum := knownSHA256[name]
	if st, e := os.Stat(dest); e == nil && st.Size() > 1<<20 {
		if err := verifyCached(ctx, dest, sum); err != nil {
			env.Logf(ctx, "warn", "缓存 ISO 校验失败，重新下载 / cached ISO failed verification: %v", err)
			os.Remove(dest)
		} else {
			env.Logf(ctx, "info", "使用已缓存 ISO / cached ISO: %s (%.1f GB)", dest, float64(st.Size())/(1<<30))
			return dest, false, "", nil
		}
	}
	env.Logf(ctx, "info", "下载安装镜像 / downloading ISO: %s", c.ISOURL)
	if err := downloadFile(ctx, c.ISOURL, dest); err != nil {
		return "", true, fmt.Sprintf("应用内下载 ISO 失败（%v）。请重试创建以自动续传；若本机已有镜像，可在高级选项填写 iso_path：%s", err, c.ISOURL), nil
	}
	if err := verifyCached(ctx, dest, sum); err != nil {
		os.Remove(dest)
		return "", false, "", fmt.Errorf("hyperv: ISO 校验失败（已删除，请重试）: %w", err)
	}
	return dest, false, "", nil
}

// verifyCached checks the SHA-256 once and leaves a "<iso>.sha256ok" marker.
func verifyCached(ctx context.Context, path, want string) error {
	if want == "" {
		return nil
	}
	marker := path + ".sha256ok"
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == want {
		return nil
	}
	env.Logf(ctx, "info", "校验 ISO SHA-256 / verifying %s", filepath.Base(path))
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 %s ≠ %s", got, want)
	}
	return os.WriteFile(marker, []byte(want+"\n"), 0o644)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// downloadFile fetches url into dest via dest.partial, resuming an earlier
// partial download with a Range request, retrying transient errors.
func downloadFile(ctx context.Context, url, dest string) error {
	var last error
	for attempt := 1; attempt <= 8; attempt++ {
		err := downloadOnce(ctx, url, dest)
		if err == nil {
			return nil
		}
		last = err
		if ctx.Err() != nil || strings.Contains(err.Error(), "HTTP 4") {
			return err
		}
		env.Logf(ctx, "warn", "下载中断，%d 秒后续传 / download interrupted (%v), resuming", attempt*5, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*5) * time.Second):
		}
	}
	return last
}

func downloadOnce(ctx context.Context, url, dest string) error {
	tmp := dest + ".partial"
	var have int64
	if st, err := os.Stat(tmp); err == nil {
		have = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AgentOS-hyperv/0.1.5")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		have = 0
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
		return os.Rename(tmp, dest) // already complete
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	total := have + resp.ContentLength
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				total = n
			}
		}
	}
	f, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	written := have
	start, lastLog, startBytes := time.Now(), time.Now(), have
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if time.Since(lastLog) > 10*time.Second && (total <= 0 || total > 64<<20) {
				speed := float64(written-startBytes) / time.Since(start).Seconds() / (1 << 20)
				if total > 0 {
					eta := time.Duration(float64(total-written)/(speed*(1<<20))) * time.Second
					env.Logf(ctx, "info", "下载进度 / download %.0f%% (%.2f / %.2f GB, %.1f MB/s, 剩余约 %s)",
						100*float64(written)/float64(total), float64(written)/(1<<30), float64(total)/(1<<30), speed, eta.Round(time.Second))
				} else {
					env.Logf(ctx, "info", "下载进度 / download %.2f GB (%.1f MB/s)", float64(written)/(1<<30), speed)
				}
				lastLog = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if total > 0 && written != total {
		return fmt.Errorf("short download %d/%d", written, total)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// cachedTool downloads a small guest tool (MSI) into <store>/cache once.
func cachedTool(ctx context.Context, store, url, name string) (string, error) {
	dir := filepath.Join(store, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, name)
	if st, err := os.Stat(dest); err == nil && st.Size() > 100<<10 {
		return dest, nil
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := downloadFile(c, url, dest); err != nil {
		return "", err
	}
	return dest, nil
}
