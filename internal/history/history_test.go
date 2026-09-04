package history

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.jsonl")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Append(Entry{Stmt: "SELECT * FROM Unit", Rows: 3})
	_ = s.Append(Entry{Stmt: "SELECT * FROM Space", Rows: 1})
	_ = s.Append(Entry{Stmt: "SELECT *  FROM Unit", Rows: 3})
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	r := s2.Recent("", 10)
	if len(r) != 2 || r[0].Stmt != "SELECT *  FROM Unit" || r[1].Stmt != "SELECT * FROM Space" {
		t.Errorf("recent: %+v", r)
	}
	if got := s2.Recent("from space", 10); len(got) != 1 {
		t.Errorf("filter: %+v", got)
	}
	if s2.At(0).Stmt != "SELECT *  FROM Unit" {
		t.Errorf("At(0): %+v", s2.At(0))
	}
}
