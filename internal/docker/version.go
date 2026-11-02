// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// imageMeta is the part of an image inspect the list needs. Images are
// immutable, so entries are cached by image id for the process lifetime.
type imageMeta struct {
	Labels map[string]string
	failed time.Time // inspect failed; retried after imageRetry
}

const imageRetry = time.Minute

// splitRef splits "lscr.io/linuxserver/plex:1.2" into ("lscr.io/linuxserver/plex", "1.2").
// The tag defaults to "latest"; it is "" for digest-pinned refs and images
// referenced by id.
func splitRef(image string) (repo, tag string) {
	if strings.HasPrefix(image, "sha256:") {
		return image, ""
	}
	if i := strings.Index(image, "@"); i >= 0 {
		repo = image[:i]
		if j := strings.LastIndex(repo, ":"); j > strings.LastIndex(repo, "/") {
			return repo[:j], repo[j+1:]
		}
		return repo, ""
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}

// lsioVersionRe matches linuxserver.io build_version labels such as
// "Linuxserver.io version:- 1.32.0.4567-ls12 Build-date:- 2024-01-01".
var lsioVersionRe = regexp.MustCompile(`(?i)version:-\s*(\S+)`)

// meaningless are label values that don't identify a version.
func meaningless(v string) bool {
	switch strings.ToLower(v) {
	case "", "latest", "stable", "main", "master", "edge", "nightly", "dev", "develop", "unknown", "null", "none", "-":
		return true
	}
	return false
}

// imageVersion returns a best-effort human version for an image: the OCI
// version label, the linuxserver build_version, a plain "version" label,
// a non-"latest" tag, else the short image id.
func imageVersion(labels map[string]string, tag, imageID string) string {
	if v := strings.TrimSpace(labels["org.opencontainers.image.version"]); !meaningless(v) {
		return truncate(v, 64)
	}
	if bv := labels["build_version"]; bv != "" {
		if m := lsioVersionRe.FindStringSubmatch(bv); m != nil && !meaningless(m[1]) {
			return truncate(m[1], 64)
		}
	}
	for _, k := range []string{"version", "org.label-schema.version"} {
		if v := strings.TrimSpace(labels[k]); !meaningless(v) && len(v) <= 64 && !strings.ContainsAny(v, " \t\n") {
			return v
		}
	}
	if tag != "" && !meaningless(tag) {
		return tag
	}
	return shortID(imageID)
}

// imageLabels returns the labels of each image id, inspecting (3 at a
// time) only ids not cached yet.
func (s *Service) imageLabels(ctx context.Context, ids []string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(ids))
	var missing []string
	s.mu.Lock()
	for _, id := range ids {
		m, ok := s.images[id]
		switch {
		case ok && m.failed.IsZero():
			out[id] = m.Labels
		case ok && time.Since(m.failed) < imageRetry:
		default:
			missing = append(missing, id)
		}
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return out
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 3)
	for _, id := range missing {
		if !validImage(id) {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			var img struct {
				Config struct {
					Labels map[string]string `json:"Labels"`
				} `json:"Config"`
			}
			err := s.c.call(cctx, http.MethodGet, "/images/"+id+"/json", nil, &img)
			m := imageMeta{Labels: img.Config.Labels}
			if err != nil {
				m = imageMeta{failed: time.Now()}
			}
			mu.Lock()
			if err == nil {
				out[id] = m.Labels
			}
			mu.Unlock()
			s.mu.Lock()
			s.images[id] = m
			s.mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}

// enrich fills version/tag/icon fields from the raw list and image labels.
func (s *Service) enrich(ctx context.Context, raw []apiContainer, l []Container) {
	ids := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, a := range raw {
		if a.ImageID != "" && !seen[a.ImageID] {
			seen[a.ImageID] = true
			ids = append(ids, a.ImageID)
		}
	}
	labels := s.imageLabels(ctx, ids)
	byID := make(map[string]*apiContainer, len(raw))
	for i := range raw {
		byID[raw[i].ID] = &raw[i]
	}
	for i := range l {
		c := &l[i]
		c.ImageRef, c.Tag = splitRef(c.Image)
		a := byID[c.FullID]
		il, ok := labels[c.ImageID]
		if !ok && a != nil {
			// Container labels include the image's; good enough until the
			// image inspect succeeds.
			il = a.Labels
		}
		c.Version = imageVersion(il, c.Tag, c.ImageID)
		if a == nil || s.icons == nil {
			continue
		}
		if srcs := iconCandidates(a); len(srcs) > 0 && !s.icons.allMissed(srcs) {
			c.Icon = "/api/containers/" + c.ID + "/icon"
		}
	}
}
