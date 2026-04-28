package predicates

import (
	"fmt"
	"strconv"
	"strings"
)

// PhaseIndexIn returns true when n is contained in the range expression.
// Range syntax: integer ("3"), closed range ("0-3"), or comma-separated
// list of either ("0-3,7,9-11"). Whitespace is tolerated. Plan 131 (I3).
func PhaseIndexIn(n int, expr string) (bool, error) {
	if expr == "" {
		return false, fmt.Errorf("phase_index_in: empty expression")
	}
	parts := strings.Split(expr, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Range "a-b"?
		if dash := strings.Index(p, "-"); dash > 0 {
			lo, err := strconv.Atoi(strings.TrimSpace(p[:dash]))
			if err != nil {
				return false, fmt.Errorf("phase_index_in: invalid lower bound %q: %w", p[:dash], err)
			}
			hi, err := strconv.Atoi(strings.TrimSpace(p[dash+1:]))
			if err != nil {
				return false, fmt.Errorf("phase_index_in: invalid upper bound %q: %w", p[dash+1:], err)
			}
			if lo > hi {
				return false, fmt.Errorf("phase_index_in: range %q has lo > hi", p)
			}
			if n >= lo && n <= hi {
				return true, nil
			}
			continue
		}
		// Single integer.
		k, err := strconv.Atoi(p)
		if err != nil {
			return false, fmt.Errorf("phase_index_in: invalid integer %q: %w", p, err)
		}
		if n == k {
			return true, nil
		}
	}
	return false, nil
}
