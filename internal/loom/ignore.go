package loom

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreMatcher evaluates relative paths against ignore rules.
type IgnoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern  string
	negation bool
	dirOnly  bool
}

// DefaultIgnorePatterns are applied if not explicitly overridden.
var defaultIgnorePatterns = []string{
	".git",
	".git/**",
	"node_modules",
	"node_modules/**",
	".DS_Store",
	"Thumbs.db",
	"*.tmp",
	"*.part",
	".loom-part-*",
	".loom",
	".loom/**",
}

// NewIgnoreMatcher creates a matcher with default patterns.
func NewIgnoreMatcher() *IgnoreMatcher {
	m := &IgnoreMatcher{}
	for _, p := range defaultIgnorePatterns {
		m.AddRule(p)
	}
	return m
}

// LoadIgnoreMatcher loads ignore rules from .loomignore and .gitignore in the folder root.
func LoadIgnoreMatcher(rootPath string) *IgnoreMatcher {
	m := NewIgnoreMatcher()

	// 1. Try .gitignore
	if f, err := os.Open(filepath.Join(rootPath, ".gitignore")); err == nil {
		m.loadFromReader(f)
		_ = f.Close()
	}

	// 2. Try .loomignore (takes precedence / adds to rules)
	if f, err := os.Open(filepath.Join(rootPath, ".loomignore")); err == nil {
		m.loadFromReader(f)
		_ = f.Close()
	}

	return m
}

func (m *IgnoreMatcher) loadFromReader(r *os.File) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m.AddRule(line)
	}
}

// AddRule parses and registers an ignore rule.
func (m *IgnoreMatcher) AddRule(rule string) {
	rule = strings.TrimSpace(rule)
	if rule == "" || strings.HasPrefix(rule, "#") {
		return
	}

	negation := false
	if strings.HasPrefix(rule, "!") {
		negation = true
		rule = strings.TrimPrefix(rule, "!")
	}

	dirOnly := false
	if strings.HasSuffix(rule, "/") {
		dirOnly = true
		rule = strings.TrimSuffix(rule, "/")
	}

	// Normalize slashes
	rule = filepath.ToSlash(rule)
	rule = strings.TrimPrefix(rule, "./")

	m.patterns = append(m.patterns, ignorePattern{
		pattern:  rule,
		negation: negation,
		dirOnly:  dirOnly,
	})
}

// ShouldIgnore tests if relPath (slash-separated relative path) matches ignore rules.
func (m *IgnoreMatcher) ShouldIgnore(relPath string, isDir bool) bool {
	cleanPath := filepath.ToSlash(relPath)
	cleanPath = strings.TrimPrefix(cleanPath, "/")

	// Never ignore .loomignore file itself
	if cleanPath == ".loomignore" {
		return false
	}

	base := filepath.Base(cleanPath)
	ignored := false

	// Build list of parent directories for prefix matching
	parts := strings.Split(cleanPath, "/")

	for _, p := range m.patterns {
		if p.dirOnly {
			// Check if the path itself is a directory and matches
			if isDir && matchPattern(p.pattern, cleanPath, base) {
				ignored = !p.negation
				continue
			}
			// Check if any parent component of this path matches the directory pattern
			matchedParent := false
			for i := 1; i < len(parts); i++ {
				parentPath := strings.Join(parts[:i], "/")
				parentBase := parts[i-1]
				if matchPattern(p.pattern, parentPath, parentBase) {
					matchedParent = true
					break
				}
			}
			if matchedParent {
				ignored = !p.negation
			}
			continue
		}

		matched := matchPattern(p.pattern, cleanPath, base)
		if matched {
			ignored = !p.negation
		}
	}

	return ignored
}

func matchPattern(pattern, relPath, base string) bool {
	// If pattern contains a slash, match against entire relative path
	if strings.Contains(pattern, "/") {
		if pattern == relPath {
			return true
		}
		// Prefix match with glob
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if relPath == prefix || strings.HasPrefix(relPath, prefix+"/") {
				return true
			}
		}
		if ok, _ := filepath.Match(pattern, relPath); ok {
			return true
		}
		// Match any subdirectory
		parts := strings.Split(relPath, "/")
		for i := 0; i < len(parts); i++ {
			sub := strings.Join(parts[i:], "/")
			if ok, _ := filepath.Match(pattern, sub); ok {
				return true
			}
		}
		return false
	}

	// Pattern without slash matches base name or any segment in path
	if ok, _ := filepath.Match(pattern, base); ok {
		return true
	}
	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		if ok, _ := filepath.Match(pattern, part); ok {
			return true
		}
	}
	return false
}
