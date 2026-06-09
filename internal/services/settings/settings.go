// Package settings provides a cached business_settings store replicating Laravel's
// Cache::remember('business_settings', 86400, ...) pattern.
package settings

import (
	"sync"
	"time"

	"gorm.io/gorm"
	"mall/internal/models"
)

type Store struct {
	db        *gorm.DB
	mu        sync.RWMutex
	cache     map[string]string // "type" -> "value"  (default lang)
	langCache map[string]map[string]string // lang -> type -> value
	expiresAt time.Time
	ttl       time.Duration
}

func New(db *gorm.DB) *Store {
	return &Store{
		db:        db,
		cache:     make(map[string]string),
		langCache: make(map[string]map[string]string),
		ttl:       24 * time.Hour,
	}
}

func (s *Store) load() {
	var rows []models.BusinessSetting
	s.db.Find(&rows)
	cache := make(map[string]string)
	langCache := make(map[string]map[string]string)
	for _, r := range rows {
		val := ""
		if r.Value != nil {
			val = *r.Value
		}
		if r.Lang == nil || *r.Lang == "" {
			cache[r.Type] = val
		} else {
			lang := *r.Lang
			if langCache[lang] == nil {
				langCache[lang] = make(map[string]string)
			}
			langCache[lang][r.Type] = val
		}
	}
	s.cache = cache
	s.langCache = langCache
	s.expiresAt = time.Now().Add(s.ttl)
}

// ensureWrite reloads the cache if it has expired. Must be called with mu held for writing.
func (s *Store) ensureWrite() {
	if time.Now().Before(s.expiresAt) {
		return
	}
	s.load()
}

// warm returns true when the cache is still valid (no lock held).
func (s *Store) warm() bool {
	return time.Now().Before(s.expiresAt)
}

// Get returns a setting value (default lang). Returns def if not found or empty.
func (s *Store) Get(key string, def ...string) string {
	// Fast path: read-lock when cache is warm.
	s.mu.RLock()
	if s.warm() {
		val, ok := s.cache[key]
		s.mu.RUnlock()
		if !ok || val == "" {
			if len(def) > 0 {
				return def[0]
			}
			return ""
		}
		return val
	}
	s.mu.RUnlock()

	// Slow path: reload under write-lock.
	s.mu.Lock()
	s.ensureWrite() // double-check after acquiring write-lock
	val, ok := s.cache[key]
	s.mu.Unlock()
	if !ok || val == "" {
		if len(def) > 0 {
			return def[0]
		}
		return ""
	}
	return val
}

// GetLang returns a lang-specific setting, falling back to the default lang.
func (s *Store) GetLang(key, lang string, def ...string) string {
	// Fast path: read-lock when cache is warm.
	s.mu.RLock()
	if s.warm() {
		if lmap, ok := s.langCache[lang]; ok {
			if v, ok2 := lmap[key]; ok2 {
				s.mu.RUnlock()
				return v
			}
		}
		val, ok := s.cache[key]
		s.mu.RUnlock()
		if !ok {
			if len(def) > 0 {
				return def[0]
			}
			return ""
		}
		return val
	}
	s.mu.RUnlock()

	// Slow path: reload under write-lock.
	s.mu.Lock()
	s.ensureWrite()
	if lmap, ok := s.langCache[lang]; ok {
		if v, ok2 := lmap[key]; ok2 {
			s.mu.Unlock()
			return v
		}
	}
	val, ok := s.cache[key]
	s.mu.Unlock()
	if !ok {
		if len(def) > 0 {
			return def[0]
		}
		return ""
	}
	return val
}

// Set saves a setting to DB and invalidates the cache.
func (s *Store) Set(key, value string) error {
	err := s.db.Model(&models.BusinessSetting{}).
		Where("type = ? AND (lang IS NULL OR lang = '')", key).
		Assign(models.BusinessSetting{Value: &value}).
		FirstOrCreate(&models.BusinessSetting{Type: key}).Error
	if err == nil {
		s.Invalidate()
	}
	return err
}

// Invalidate forces a reload on next access.
func (s *Store) Invalidate() {
	s.mu.Lock()
	s.expiresAt = time.Time{}
	s.mu.Unlock()
}
