package predicates

import "testing"

func TestPhaseIndexIn(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		expr    string
		want    bool
		wantErr bool
	}{
		{"single match", 3, "3", true, false},
		{"single no match", 3, "5", false, false},
		{"range inclusive lower", 0, "0-3", true, false},
		{"range inclusive upper", 3, "0-3", true, false},
		{"range middle", 2, "0-3", true, false},
		{"range outside", 4, "0-3", false, false},
		{"comma list match first", 7, "0-3,7,9-11", true, false},
		{"comma list match range", 10, "0-3,7,9-11", true, false},
		{"comma list miss", 5, "0-3,7,9-11", false, false},
		{"whitespace tolerant", 3, " 0 - 3 ", true, false},
		{"empty expr is error", 0, "", false, true},
		{"invalid integer", 0, "not-a-number", false, true},
		{"reversed range", 0, "5-3", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PhaseIndexIn(tt.n, tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PhaseIndexIn(%d, %q) error = %v, wantErr=%v", tt.n, tt.expr, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("PhaseIndexIn(%d, %q) = %v, want %v", tt.n, tt.expr, got, tt.want)
			}
		})
	}
}
