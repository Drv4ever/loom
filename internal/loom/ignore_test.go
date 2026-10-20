package loom

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoreMatcherDefaults(t *testing.T) {
	m := NewIgnoreMatcher()

	cases := []struct {
		path   string
		isDir  bool
		ignore bool
	}{
		{".git/config", false, true},
		{"node_modules/react/index.js", false, true},
		{".DS_Store", false, true},
		{"src/App.tsx", false, false},
		{"build.tmp", false, true},
		{".loom-part-xyz", false, true},
		{".loomignore", false, false},
	}

	for _, c := range cases {
		got := m.ShouldIgnore(c.path, c.isDir)
		if got != c.ignore {
			t.Errorf("ShouldIgnore(%q, %v) = %v; want %v", c.path, c.isDir, got, c.ignore)
		}
	}
}

func TestIgnoreMatcherFromFile(t *testing.T) {
	dir := t.TempDir()
	content := "*.log\nbuild/\n!important.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".loomignore"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	m := LoadIgnoreMatcher(dir)
	if !m.ShouldIgnore("test.log", false) {
		t.Errorf("test.log should be ignored")
	}
	if m.ShouldIgnore("important.log", false) {
		t.Errorf("important.log should not be ignored (negation)")
	}
	if !m.ShouldIgnore("build/app.exe", false) {
		t.Errorf("build/app.exe should be ignored")
	}
	if m.ShouldIgnore("src/main.go", false) {
		t.Errorf("src/main.go should not be ignored")
	}
}
