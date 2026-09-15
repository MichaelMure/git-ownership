package main

import "testing"

func TestGlobPattern(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// No "/": matches a name at any depth, file or directory.
		{"vendor", "vendor/a.go", true},
		{"vendor", "a/vendor/b/c.go", true},
		{"vendor", "vendor", true},
		{"vendor", "vendorx/a.go", false},
		{"*.pb.go", "api/v1/foo.pb.go", true},
		{"*.pb.go", "foo.pb.go", true},
		{"*.pb.go", "foo.go", false},

		// Trailing "/": directories only.
		{"vendor/", "vendor/a.go", true},
		{"vendor/", "a/vendor/b.go", true},
		{"vendor/", "vendor", false},
		{"vendor/", "a/vendor", false},

		// Leading or inner "/": anchored to the repo root.
		{"/vendor/", "vendor/a.go", true},
		{"/vendor/", "a/vendor/b.go", false},
		{"/main.go", "main.go", true},
		{"/main.go", "cmd/main.go", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/sub/a.md", false},
		{"docs/*.md", "x/docs/a.md", false},
		{"pkg/api", "pkg/api/a.go", true},
		{"pkg/api", "pkg/apix/a.go", false},

		// "**".
		{"**/testdata/**", "testdata/a.txt", true},
		{"**/testdata/**", "a/b/testdata/c/d.txt", true},
		{"**/testdata/**", "a/testdatax/d.txt", false},
		{"a/**/b", "a/b/c.go", true},
		{"a/**/b", "a/x/y/b/c.go", true},
		{"a/**/b", "x/a/b/c.go", false},
		{"a/**/*.go", "a/x/y/c.go", true},
		{"a/**/*.go", "a/x/y/c.txt", false},
		{"**", "anything/at/all.go", true},
		{"/**/", "root.go", false},
		{"/**/", "dir/file.go", true},
	}
	for _, tt := range tests {
		g, err := parseGlob(tt.pattern)
		if err != nil {
			t.Fatalf("parseGlob(%q): %v", tt.pattern, err)
		}
		f := &pathFilter{excludes: []globPattern{g}}
		if got := f.excluded(tt.path); got != tt.want {
			t.Errorf("pattern %q, path %q: got %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestParseGlobErrors(t *testing.T) {
	for _, p := range []string{"!vendor", "/", "[a-", "a/[/b"} {
		if _, err := parseGlob(p); err == nil {
			t.Errorf("parseGlob(%q): expected error", p)
		}
	}
}

func TestPathFilter(t *testing.T) {
	f, err := newPathFilter(
		[]string{"/pkg/", "/cmd/", ""},
		[]string{"testdata/", "*_gen.go"},
		[]string{"^cmd/legacy/", ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"pkg/a.go", false},
		{"cmd/tool/main.go", false},
		{"main.go", true},            // not included
		{"internal/x.go", true},      // not included
		{"pkg/testdata/a.txt", true}, // excluded by glob
		{"pkg/model_gen.go", true},   // excluded by glob
		{"cmd/legacy/main.go", true}, // excluded by regex
	}
	for _, tt := range tests {
		if got := f.excluded(tt.path); got != tt.want {
			t.Errorf("excluded(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}

	empty, err := newPathFilter(nil, []string{""}, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if empty.excluded("any/file.go") {
		t.Error("empty flag values should not exclude anything")
	}

	if _, err := newPathFilter(nil, nil, []string{"("}); err == nil {
		t.Error("expected error for invalid regex")
	}
}
