package isureal

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const banFileLead = 100 * time.Millisecond

type banFileSyncer struct {
	dir   string
	mu    sync.Mutex
	timer *time.Timer
}

func newBanFileSyncer(dir string) *banFileSyncer {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("banFileSyncer: mkdir %s: %v", dir, err)
		return nil
	}
	return &banFileSyncer{dir: dir}
}

func validBanFileName(ip string) bool {
	if len(ip) < 1 || len(ip) > 45 || ip == "." || ip == ".." {
		return false
	}
	for i := 0; i < len(ip); i++ {
		c := ip[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		case c == '.' || c == ':':
		default:
			return false
		}
	}
	return true
}

func (s *banFileSyncer) sync(source func() []map[string]time.Time, onExpire func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var bans []map[string]time.Time
	if source != nil {
		bans = source()
	}
	now := ipBanNow()

	merged := make(map[string]time.Time)
	for _, m := range bans {
		for ip, until := range m {
			if cur, ok := merged[ip]; !ok || until.After(cur) {
				merged[ip] = until
			}
		}
	}

	desired := make(map[string]time.Time)
	var earliest time.Time
	for ip, until := range merged {
		if !validBanFileName(ip) {
			continue
		}
		if !now.Add(banFileLead).Before(until) {
			continue
		}
		desired[ip] = until
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		log.Printf("banFileSyncer: readdir %s: %v", s.dir, err)
	}
	existing := make(map[string]bool)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if _, ok := desired[name]; ok {
			existing[name] = true
		} else {
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
				log.Printf("banFileSyncer: remove %s: %v", name, err)
			}
		}
	}

	for ip := range desired {
		if !existing[ip] {
			path := filepath.Join(s.dir, ip)
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				log.Printf("banFileSyncer: write %s: %v", path, err)
			}
		}
	}

	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if len(desired) > 0 && onExpire != nil {
		delay := earliest.Sub(now) - banFileLead
		if delay < 0 {
			delay = 0
		}
		s.timer = time.AfterFunc(delay, onExpire)
	}
}

func (s *banFileSyncer) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}
