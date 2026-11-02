// SPDX-License-Identifier: AGPL-3.0-only

package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/dashicons"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/netx"
)

const maxIconSize = dashicons.MaxSize

var (
	iconNameRe = regexp.MustCompile(`^[a-f0-9]{64}\.(png|svg|webp|jpg)$`)
)

func validIconName(n string) bool { return iconNameRe.MatchString(n) }

func sniffIcon(b []byte) string { return dashicons.Sniff(b) }

// saveIcon stores b content-addressed and returns its file name.
func (s *Service) saveIcon(b []byte) (string, error) {
	if len(b) == 0 {
		return "", httpx.BadRequest("empty file")
	}
	if len(b) > maxIconSize {
		return "", httpx.BadRequest("icon is larger than 512 KB")
	}
	ext := sniffIcon(b)
	if ext == "" {
		return "", httpx.BadRequest("unsupported image type (use PNG, SVG, WebP or JPEG)")
	}
	sum := sha256.Sum256(b)
	name := hex.EncodeToString(sum[:]) + "." + ext
	if err := os.MkdirAll(s.iconDir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(s.iconDir, name)
	if _, err := os.Stat(dst); err == nil {
		return name, nil
	}
	tmp, err := os.CreateTemp(s.iconDir, ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return name, os.Rename(tmp.Name(), dst)
}

func (s *Service) handleIconUpload(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxIconSize+64<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		return httpx.BadRequest("expected multipart/form-data")
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return httpx.BadRequest("missing file")
		}
		if err != nil {
			return httpx.BadRequest("upload too large or malformed")
		}
		if p.FormName() != "file" {
			p.Close()
			continue
		}
		b, err := io.ReadAll(io.LimitReader(p, maxIconSize+1))
		p.Close()
		if err != nil {
			return httpx.BadRequest("upload too large or malformed")
		}
		name, err := s.saveIcon(b)
		s.audit.Record(r, "launcher.icon_upload", name, err, nil)
		if err != nil {
			return err
		}
		httpx.OK(w, map[string]string{"icon": name})
		return nil
	}
}

// newIconClient is the client for the icon CDN: redirects are only
// followed within the CDN host, so a misbehaving CDN can't point Wargo at
// internal services.
func newIconClient() *http.Client {
	c := newHTTPClient(15 * time.Second)
	c.CheckRedirect = netx.SameHostRedirects(3)
	return c
}

// fetchIcon downloads a dashboard-icons PNG by slug.
func (s *Service) fetchIcon(ctx context.Context, slug string) (string, error) {
	if !dashicons.ValidSlug(slug) {
		return "", httpx.BadRequest("invalid icon name (lowercase letters, digits and dashes)")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.iconBase+slug+".png", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Wargo")
	resp, err := s.iconClient.Do(req)
	if err != nil {
		return "", httpx.Errorf(http.StatusBadGateway, "fetch_failed", "Could not reach the icon CDN")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", httpx.Errorf(http.StatusNotFound, "icon_not_found", fmt.Sprintf("No icon named %q in dashboard-icons", slug))
	}
	if resp.StatusCode != http.StatusOK {
		return "", httpx.Errorf(http.StatusBadGateway, "fetch_failed", "Icon CDN answered "+resp.Status)
	}
	b, err := netx.ReadLimited(resp.Body, maxIconSize)
	if errors.Is(err, netx.ErrTooLarge) {
		return "", httpx.BadRequest("icon is larger than 512 KB")
	}
	if err != nil {
		return "", httpx.Errorf(http.StatusBadGateway, "fetch_failed", "Icon download failed")
	}
	if sniffIcon(b) != "png" {
		return "", httpx.Errorf(http.StatusBadGateway, "fetch_failed", "CDN did not return a PNG")
	}
	return s.saveIcon(b)
}

func (s *Service) handleIconFetch(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Slug string `json:"slug"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	name, err := s.fetchIcon(r.Context(), slug)
	s.audit.Record(r, "launcher.icon_fetch", slug, err, nil)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]string{"icon": name})
	return nil
}

func (s *Service) handleIconGet(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if !validIconName(name) {
		return httpx.ErrNotFound
	}
	root, err := os.OpenRoot(s.iconDir)
	if err != nil {
		return httpx.ErrNotFound
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return httpx.ErrNotFound
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return httpx.ErrNotFound
	}
	ext := name[strings.LastIndexByte(name, '.')+1:]
	h := w.Header()
	h.Set("Content-Type", dashicons.ContentType(ext))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	if ext == "svg" {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
	return nil
}
