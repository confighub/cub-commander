// Package history is the per-user statement history: one JSON line per
// executed statement in ~/.confighub/commander/history.jsonl.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Entry struct {
	Time  time.Time `json:"time"`
	Stmt  string    `json:"stmt"`
	Space string    `json:"space,omitempty"`
	Rows  int       `json:"rows"`
	Err   string    `json:"err,omitempty"`
}

type Store struct {
	path    string
	entries []Entry // oldest first
}

// Open loads the history file, creating its directory. A missing file is fine.
func Open(path string) (*Store, error) {
	if path == "" {
		dir := os.Getenv("CUB_CONFIG")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".confighub")
		}
		path = filepath.Join(dir, "commander", "history.jsonl")
	}
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Stmt != "" {
			s.entries = append(s.entries, e)
		}
	}
	return s, nil
}

// Append records an entry and writes it through.
func (s *Store) Append(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.entries = append(s.entries, e)
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

// Len is the number of entries.
func (s *Store) Len() int { return len(s.entries) }

// At returns the i-th most recent entry (0 = newest).
func (s *Store) At(i int) Entry { return s.entries[len(s.entries)-1-i] }

// Recent returns distinct statements, newest first, optionally filtered by a
// case-insensitive substring match on every space-separated word.
func (s *Store) Recent(filter string, limit int) []Entry {
	words := strings.Fields(strings.ToLower(filter))
	seen := map[string]bool{}
	var out []Entry
	for i := len(s.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.entries[i]
		key := strings.Join(strings.Fields(e.Stmt), " ")
		if seen[key] {
			continue
		}
		low := strings.ToLower(key)
		ok := true
		for _, w := range words {
			if !strings.Contains(low, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}
