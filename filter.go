package main

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// pathFilter decides which file paths are tracked. A path is excluded when
// include patterns are given and it matches none of them, or when it matches
// any exclude pattern or exclude regex.
type pathFilter struct {
	includes  []globPattern
	excludes  []globPattern
	excludeRe []*regexp.Regexp
}

// newPathFilter parses the raw flag values. Empty values are ignored, so an
// unset shell variable passed as a flag value doesn't exclude everything.
func newPathFilter(includes, excludes, excludeRegexes []string) (*pathFilter, error) {
	f := &pathFilter{}
	for _, p := range includes {
		if p == "" {
			continue
		}
		g, err := parseGlob(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --include %q: %w", p, err)
		}
		f.includes = append(f.includes, g)
	}
	for _, p := range excludes {
		if p == "" {
			continue
		}
		g, err := parseGlob(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --exclude %q: %w", p, err)
		}
		f.excludes = append(f.excludes, g)
	}
	for _, r := range excludeRegexes {
		if r == "" {
			continue
		}
		re, err := regexp.Compile(r)
		if err != nil {
			return nil, fmt.Errorf("invalid --exclude-regex: %w", err)
		}
		f.excludeRe = append(f.excludeRe, re)
	}
	return f, nil
}

// excluded reports whether file (a repo-relative, slash-separated path)
// should be ignored.
func (f *pathFilter) excluded(file string) bool {
	if len(f.includes) > 0 || len(f.excludes) > 0 {
		parts := strings.Split(file, "/")
		if len(f.includes) > 0 && !matchAnyGlob(f.includes, parts) {
			return true
		}
		if matchAnyGlob(f.excludes, parts) {
			return true
		}
	}
	for _, re := range f.excludeRe {
		if re.MatchString(file) {
			return true
		}
	}
	return false
}

func matchAnyGlob(globs []globPattern, parts []string) bool {
	for _, g := range globs {
		if g.match(parts) {
			return true
		}
	}
	return false
}

// globPattern is a parsed gitignore-style pattern:
//   - a pattern without "/" matches a file or directory name at any depth;
//   - a pattern containing "/" is relative to the repo root;
//   - a trailing "/" only matches directories;
//   - "*", "?" and "[...]" match within a single path component;
//   - "**" as a whole component matches any number of directories.
//
// Matching a directory matches every file below it. Negation ("!") is not
// supported.
type globPattern struct {
	segs    []string // pattern split on "/"
	dirOnly bool
}

func parseGlob(p string) (globPattern, error) {
	if strings.HasPrefix(p, "!") {
		return globPattern{}, errors.New("negation is not supported")
	}
	var g globPattern
	if strings.HasSuffix(p, "/") {
		g.dirOnly = true
		p = strings.TrimRight(p, "/")
	}
	if !strings.Contains(p, "/") {
		p = "**/" + p
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "**/" {
		return globPattern{}, errors.New("empty pattern")
	}
	g.segs = strings.Split(p, "/")
	for _, s := range g.segs {
		// path.Match validates the whole pattern even when it doesn't match.
		if _, err := path.Match(s, ""); err != nil {
			return globPattern{}, err
		}
	}
	return g, nil
}

// match reports whether the path split into parts, or one of its parent
// directories, matches g.
func (g globPattern) match(parts []string) bool {
	if g.dirOnly {
		// Only parent directories are candidates, never the file itself.
		parts = parts[:len(parts)-1]
		if len(parts) == 0 {
			return false
		}
	}
	return matchPrefix(g.segs, parts)
}

// matchPrefix reports whether pattern segments pat match a leading portion
// of the path components parts.
func matchPrefix(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(parts); i++ {
				if matchPrefix(pat[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], parts[0]); !ok {
			return false
		}
		pat, parts = pat[1:], parts[1:]
	}
	return true
}
