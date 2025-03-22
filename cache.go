// Package plugin_simplecache is a plugin to cache responses to disk.
package plugin_simplecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pquerna/cachecontrol"
)

// Config configures the middleware.
type Config struct {
	Path            string `json:"path" yaml:"path" toml:"path"`
	MaxExpiry       int    `json:"maxExpiry" yaml:"maxExpiry" toml:"maxExpiry"`
	Cleanup         int    `json:"cleanup" yaml:"cleanup" toml:"cleanup"`
	AddStatusHeader bool   `json:"addStatusHeader" yaml:"addStatusHeader" toml:"addStatusHeader"`
}

// CreateConfig returns a config instance.
func CreateConfig() *Config {
	return &Config{
		MaxExpiry:       int((5 * time.Minute).Seconds()),
		Cleanup:         int((5 * time.Minute).Seconds()),
		AddStatusHeader: true,
	}
}

const (
	cacheHeader      = "Cache-Status"
	cacheHitStatus   = "hit"
	cacheMissStatus  = "miss"
	cacheErrorStatus = "error"
	cleanupDisabled  = -1
)

type cache struct {
	name  string
	cache *fileCache
	cfg   *Config
	next  http.Handler
}

// New returns a plugin instance.
func New(_ context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	if cfg.MaxExpiry <= 1 {
		return nil, errors.New("maxExpiry must be greater or equal to 1")
	}

	if cfg.Cleanup <= 1 && cfg.Cleanup != cleanupDisabled {
		return nil, fmt.Errorf("cleanup must be greater or equal to 1 or disabled %d", cleanupDisabled)
	}

	fc, err := newFileCache(cfg.Path, time.Duration(cfg.Cleanup)*time.Second)
	if err != nil {
		return nil, err
	}

	m := &cache{
		name:  name,
		cache: fc,
		cfg:   cfg,
		next:  next,
	}

	return m, nil
}

type cacheData struct {
	Status  int
	Headers map[string][]string
	Body    []byte
}

// ServeHTTP serves an HTTP request.
func (m *cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Вывод сообщения в терминал для каждого запроса
	os.Stdout.WriteString("ПАЛУНДРА, ПРИШЕЛ ЗАПРОС!!\n")

	cs := cacheMissStatus

	key := cacheKey(r)
	log.Printf("Cache key: %s", key)

	b, err := m.cache.Get(key)
	if err == nil {
		var data cacheData

		err := json.Unmarshal(b, &data)
		if err != nil {
			cs = cacheErrorStatus
			log.Printf("Error unmarshaling cache data: %v", err)
		} else {
			for key, vals := range data.Headers {
				for _, val := range vals {
					w.Header().Add(key, val)
				}
			}
			if m.cfg.AddStatusHeader {
				w.Header().Set(cacheHeader, cacheHitStatus)
			}
			w.WriteHeader(data.Status)
			_, _ = w.Write(data.Body)
			log.Printf("Cache hit for key: %s", key)
			return
		}
	} else {
		log.Printf("Cache miss for key: %s, error: %v", key, err)
	}

	if m.cfg.AddStatusHeader {
		w.Header().Set(cacheHeader, cs)
	}

	rw := &responseWriter{ResponseWriter: w}
	m.next.ServeHTTP(rw, r)

	expiry, ok := m.cacheable(r, w, rw.status)
	if !ok {
		log.Printf("Response not cacheable for key: %s", key)
		return
	}

	data := cacheData{
		Status:  rw.status,
		Headers: w.Header().Clone(), // Клонируем заголовки, чтобы избежать изменений
		Body:    rw.body,
	}

	// Удаляем заголовки, которые не должны влиять на кэш
	data.Headers.Del("Date")
	data.Headers.Del("Set-Cookie")
	data.Headers.Del("Cache-Status")

	b, err = json.Marshal(data)
	if err != nil {
		log.Printf("Error serializing cache item: %v", err)
		return
	}

	if err = m.cache.Set(key, b, expiry); err != nil {
		log.Printf("Error setting cache item: %v", err)
	} else {
		log.Printf("Cache set for key: %s with expiry: %v", key, expiry)
	}
}

func (m *cache) cacheable(r *http.Request, w http.ResponseWriter, status int) (time.Duration, bool) {
	// Принудительно кэшируем успешные ответы
	if status == http.StatusOK {
		return time.Duration(m.cfg.MaxExpiry) * time.Second, true
	}

	// Остальная логика
	reasons, expireBy, err := cachecontrol.CachableResponseWriter(r, status, w, cachecontrol.Options{})
	if err != nil || len(reasons) > 0 {
		return 0, false
	}

	expiry := time.Until(expireBy)
	maxExpiry := time.Duration(m.cfg.MaxExpiry) * time.Second

	if maxExpiry < expiry {
		expiry = maxExpiry
	}

	return expiry, true
}

func cacheKey(r *http.Request) string {
	// Включаем метод, хост, путь и query parameters в ключ
	key := r.Method + r.Host + r.URL.Path + "?" + r.URL.RawQuery
	// Включаем заголовок Authorization в ключ
	key += "|Authorization:" + r.Header.Get("Authorization")
	return key
}

type responseWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (rw *responseWriter) Header() http.Header {
	return rw.ResponseWriter.Header()
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	rw.body = append(rw.body, p...)
	return rw.ResponseWriter.Write(p)
}

func (rw *responseWriter) WriteHeader(s int) {
	rw.status = s
	rw.ResponseWriter.WriteHeader(s)
}

// fileCache реализация
type fileCache struct {
	path    string
	cleanup time.Duration
}

func newFileCache(path string, cleanup time.Duration) (*fileCache, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, err
	}
	return &fileCache{path: path, cleanup: cleanup}, nil
}

func (fc *fileCache) Get(key string) ([]byte, error) {
	filePath := filepath.Join(fc.path, key)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (fc *fileCache) Set(key string, data []byte, expiry time.Duration) error {
	filePath := filepath.Join(fc.path, key)
	return os.WriteFile(filePath, data, 0644)
}