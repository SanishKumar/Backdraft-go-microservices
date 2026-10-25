package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/SanishKumar/go-microservices/internal/jsonutil"
)

type Record struct {
	ID     string `json:"id"`
	Bundle Bundle `json:"bundle"`
	Report Report `json:"report"`
}
type Store struct {
	dir    string
	mu     sync.Mutex
	memory map[string][]byte
}

var validID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, memory: map[string][]byte{}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		f, err := os.CreateTemp(dir, ".probe-")
		if err != nil {
			return nil, err
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
	return s, nil
}
func (s *Store) Persistent() bool { return s.dir != "" }
func (s *Store) Save(b Bundle, r Report) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := b.Validate(); err != nil {
		return Record{}, err
	}
	if r.BundleDigest != Digest(b) {
		return Record{}, fmt.Errorf("report does not match bundle")
	}
	record := Record{Bundle: b, Report: r}
	record.ID = Digest(record)
	raw, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	if s.dir == "" {
		if len(s.memory) >= 200 {
			return Record{}, fmt.Errorf("temporary workspace is full; use --data to retain reviews on disk")
		}
		s.memory[record.ID] = raw
		return record, nil
	}
	f, err := os.CreateTemp(s.dir, ".review-")
	if err != nil {
		return Record{}, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return Record{}, err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return Record{}, err
	}
	if err = f.Close(); err != nil {
		return Record{}, err
	}
	path := filepath.Join(s.dir, record.ID+".json")
	if _, err = os.Stat(path); err == nil {
		return record, nil
	}
	if err = os.Rename(tmp, path); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (s *Store) Get(id string) (Record, error) {
	if !validID.MatchString(id) {
		return Record{}, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	var err error
	if s.dir == "" {
		r, ok := s.memory[id]
		if !ok {
			return Record{}, os.ErrNotExist
		}
		raw = r
	} else {
		raw, err = os.ReadFile(filepath.Join(s.dir, id+".json"))
	}
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err = jsonutil.DecodeObject(bytes.NewReader(raw), &r); err != nil {
		return Record{}, err
	}
	got := r.ID
	r.ID = ""
	want := Digest(r)
	r.ID = got
	if got != id || want != id || r.Report.BundleDigest != Digest(r.Bundle) {
		return Record{}, fmt.Errorf("review %s failed integrity verification", id)
	}
	return r, nil
}

type Summary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Report Report `json:"report"`
}

func (s *Store) List() ([]Summary, error) {
	var ids []string
	if s.dir == "" {
		s.mu.Lock()
		for id := range s.memory {
			ids = append(ids, id)
		}
		s.mu.Unlock()
	} else {
		files, err := os.ReadDir(s.dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
				ids = append(ids, f.Name()[:len(f.Name())-5])
			}
		}
	}
	out := []Summary{}
	for _, id := range ids {
		r, err := s.Get(id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		summary := r.Report
		summary.Cases = nil
		summary.Witness = nil
		summary.Changes = nil
		out = append(out, Summary{id, r.Bundle.Name, summary})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Report.CreatedAt.After(out[j].Report.CreatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}
