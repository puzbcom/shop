// Package i18n provides translation lookup from embedded JSON files.
// Each locale is locales/<lang>.json: {"normalized_key": "value", ...}
// Fallback chain: requested lang → default lang → "en" → original key.
package i18n

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"regexp"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localesFS embed.FS

var reNonAlnum = regexp.MustCompile(`[^a-z0-9_]`)

// NormalizeKey converts a display string to its storage key.
func NormalizeKey(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "_")
	s = reNonAlnum.ReplaceAllString(s, "")
	return s
}

type Service struct {
	langs       map[string]map[string]string // read-only after New
	mu          sync.RWMutex
	defaultLang string
}

func New(defaultLang string) *Service {
	if defaultLang == "" {
		defaultLang = "en"
	}
	return &Service{langs: loadAll(), defaultLang: defaultLang}
}

func loadAll() map[string]map[string]string {
	result := make(map[string]map[string]string)
	entries, err := fs.ReadDir(localesFS, "locales")
	if err != nil {
		log.Printf("i18n: readdir: %v", err)
		return result
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		lang := strings.TrimSuffix(e.Name(), ".json")
		data, err := localesFS.ReadFile("locales/" + e.Name())
		if err != nil {
			log.Printf("i18n: read %s: %v", e.Name(), err)
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("i18n: parse %s: %v", e.Name(), err)
			continue
		}
		result[lang] = m
	}
	return result
}

// Translate looks up key with fallback: lang → defaultLang → "en" → original key.
func (s *Service) Translate(key, lang string) string {
	normalized := NormalizeKey(key)

	s.mu.RLock()
	defLang := s.defaultLang
	s.mu.RUnlock()

	for _, l := range dedup(lang, defLang, "en") {
		if m, ok := s.langs[l]; ok {
			if v, ok := m[normalized]; ok && v != "" {
				return v
			}
		}
	}
	return key
}

// SetDefaultLang updates the fallback language (called per-request by Locale middleware).
func (s *Service) SetDefaultLang(lang string) {
	s.mu.Lock()
	s.defaultLang = lang
	s.mu.Unlock()
}

// dedup returns langs in order, skipping empty and duplicate values.
func dedup(langs ...string) []string {
	seen := make(map[string]bool, len(langs))
	out := make([]string, 0, len(langs))
	for _, l := range langs {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}
