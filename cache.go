// Package plugin_simplecache is a plugin to cache responses using go-cache.
package plugin_simplecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/pquerna/cachecontrol"
)

// Config configures the middleware.
type Config struct {
	MaxExpiry       int  `json:"maxExpiry" yaml:"maxExpiry" toml:"maxExpiry"`
	AddStatusHeader bool `json:"addStatusHeader" yaml:"addStatusHeader" toml:"addStatusHeader"`
}

// CreateConfig returns a config instance.
func CreateConfig() *Config {
	return &Config{
		MaxExpiry:       int((5 * time.Minute).Seconds()),
		AddStatusHeader: true,
	}
}

const (
	cacheHeader      = "Cache-Status"
	cacheHitStatus   = "hit"
	cacheMissStatus  = "miss"
	cacheErrorStatus = "error"
)

type cache struct {
	name  string
	store *cache.Cache
	cfg   *Config
	next  http.Handler
}

// New returns a plugin instance.
func New(_ context.Context, next http.Handler, cfg *Config, name string) (http.Handler, error) {
	if cfg.MaxExpiry <= 1 {
		return nil, errors.New("maxExpiry must be greater or equal to 1")
	}

	c := cache.New(time.Duration(cfg.MaxExpiry)*time.Second, 10*time.Minute)

	m := &cache{
		name:  name,
		store: c,
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
	// Log incoming request
	os.Stdout.WriteString("ПАЛУНДРА, ПРИШЕЛ ЗАПРОС!!\n")

	cs := cacheMissStatus
	key := cacheKey(r)

	if data, found := m.store.Get(key); found {
		var cachedData cacheData
		if err := json.Unmarshal(data.([]byte), &cachedData); err == nil {
			for key, vals := range cachedData.Headers {
				for _, val := range vals {
					w.Header().Add(key, val)
				}
			}
			if m.cfg.AddStatusHeader {
				w.Header().Set(cacheHeader, cacheHitStatus)
			}
			w.WriteHeader(cachedData.Status)
			_, _ = w.Write(cachedData.Body)
			return
		} else {
			cs = cacheErrorStatus
		}
	}

	if m.cfg.AddStatusHeader {
		w.Header().Set(cacheHeader, cs)
	}

	rw := &responseWriter{ResponseWriter: w}
	m.next.ServeHTTP(rw, r)

	expiry, ok := m.cacheable(r, w, rw.status)
	if !ok {
		return
	}

	data := cacheData{
		Status:  rw.status,
		Headers: w.Header(),
		Body:    rw.body,
	}

	b, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error serializing cache item: %v", err)
		return
	}

	m.store.Set(key, b, expiry)
}

func (m *cache) cacheable(r *http.Request, w http.ResponseWriter, status int) (time.Duration, bool) {
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
	return r.Method + r.Host + r.URL.Path
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
