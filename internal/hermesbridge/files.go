package hermesbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

const (
	maxUpload  = 30 << 20
	uploadKeep = 7 * 24 * time.Hour
)

var errTooBig = errors.New("첨부가 너무 커서 받지 못했다")

func (b *Bridge) uploadDir() string {
	return filepath.Join(cmpOr(b.cfg.HermesHome, os.TempDir()), "kyeot-uploads")
}

// attachNote saves attachments into a Mac folder Hermes can read and builds the note appended to the input.
// Hermes runs on the same Mac, so a path is enough for it to open the file with its own tools.
func (b *Bridge) attachNote(ctx context.Context, atts []protocol.Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	var lines []string
	for _, a := range atts {
		path, err := b.download(ctx, b.uploadDir(), a)
		switch {
		case errors.Is(err, errTooBig):
			lines = append(lines, fmt.Sprintf("- %s (%v)", a.Name, err))
		case err != nil:
			var ue *url.Error
			if errors.As(err, &ue) { // the address is not left in the Hermes input
				err = ue.Err
			}
			lines = append(lines, fmt.Sprintf("- %s (받지 못함: %v)", a.Name, err))
		default:
			lines = append(lines, fmt.Sprintf("- %s (%s, %s)", path, a.Mime, humanSize(a.Size)))
		}
	}
	return "\n\n[첨부 파일 — 아래 경로의 파일을 직접 열어 보세요]\n" + strings.Join(lines, "\n")
}

func (b *Bridge) download(ctx context.Context, dir string, a protocol.Attachment) (string, error) {
	if a.ID == "" || strings.ContainsAny(a.ID, "/\\.") || !strings.HasPrefix(a.URL, "/api/uploads/") {
		return "", fmt.Errorf("bad attachment")
	}
	if a.Size > maxUpload {
		return "", errTooBig
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second+time.Duration(max(a.Size, 0)/(64<<10))*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.cfg.DeckURL, "/")+a.URL, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("deck %d", res.StatusCode)
	}
	name := a.ID[:min(8, len(a.ID))] + "-" + strings.ReplaceAll(filepath.Base(a.Name), string(filepath.Separator), "_")
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, maxUpload+1))
	if err == nil && n > maxUpload {
		err = errTooBig
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// cleanUploads deletes attachments older than 7 days at start and once a day.
func (b *Bridge) cleanUploads(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		dir := b.uploadDir()
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > uploadKeep {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	}
	return fmt.Sprintf("%dB", n)
}

func cmpOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
