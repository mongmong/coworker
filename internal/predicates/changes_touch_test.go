package predicates

import (
	"testing"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		file    string
		want    bool
	}{
		// Exact match.
		{"main.go", "main.go", true},
		{"main.go", "other.go", false},
		// Single-star wildcard.
		{"*.go", "main.go", true},
		{"*.go", "main.ts", false},
		// Prefix/** — any depth under prefix.
		{"web/**", "web/app/Page.tsx", true},
		{"web/**", "web/index.html", true},
		{"web/**", "src/main.go", false},
		{"web/**", "web", false}, // must be under web/
		// **/<suffix> — base name at any depth.
		{"**/*.tsx", "web/app/Page.tsx", true},
		{"**/*.tsx", "Page.tsx", true},
		{"**/*.tsx", "web/app/Page.go", false},
		// Unsupported **: middle segment — no match, no error.
		{"a/**/b", "a/x/b", false},
		// filepath.Match pattern.
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/main.ts", false},
		{"src/*.go", "lib/main.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.pattern+"~"+tc.file, func(t *testing.T) {
			got, err := globMatch(tc.pattern, tc.file)
			if err != nil {
				t.Fatalf("globMatch(%q, %q): unexpected error: %v", tc.pattern, tc.file, err)
			}
			if got != tc.want {
				t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.file, got, tc.want)
			}
		})
	}
}

func TestChangesTouchInDir_EmptyGlobs(t *testing.T) {
	_, err := ChangesTouchInDir("", nil)
	if err == nil {
		t.Fatal("expected error for empty globs")
	}
}
