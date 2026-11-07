package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogWritesPrivateJSONL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sous", "audit.jsonl")
	t.Setenv("LABCTL_AUDIT", p)
	ok := true
	Log(Entry{Event: "start", Origin: "cli", Args: []string{"pentest", "up"}})
	Log(Entry{Event: "end", Origin: "cli", Args: []string{"pentest", "up"}, OK: &ok, Duration: 12})
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("droits %o, 0600 attendu", fi.Mode().Perm())
	}
	f, _ := os.Open(p)
	defer f.Close()
	var lines []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("ligne non JSON : %s", sc.Text())
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 || lines[0].Event != "start" || lines[1].OK == nil || !*lines[1].OK || lines[1].Time == "" || lines[1].PID == 0 {
		t.Fatalf("entrées inattendues : %+v", lines)
	}
}

func TestLogRotatesAndCanBeDisabled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("LABCTL_AUDIT", p)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", maxSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	Log(Entry{Event: "start"})
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() <= maxSize {
		t.Fatalf("rotation attendue vers .1 : %v", err)
	}
	if fi, _ := os.Stat(p); fi.Size() > 1000 {
		t.Fatal("le journal courant devrait repartir de zéro")
	}
	t.Setenv("LABCTL_AUDIT", "off")
	if Path() != "" {
		t.Fatal("LABCTL_AUDIT=off doit désactiver le journal")
	}
}
