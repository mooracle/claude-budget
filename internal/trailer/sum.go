package trailer

import (
	"fmt"
	"strconv"
	"strings"
)

// SumDuplicates collapses repeated bare-number cost trailers into a single line.
//
// When git concatenates several commit messages (rebase reword, squash), each
// original message contributes its own cost trailer, so the combined message ends
// up with multiple "<trailerName>: <number>" lines. SumDuplicates folds those into
// one summed line, placed where the first duplicate was, dropping the rest. Lines
// in, lines out — no I/O — so the caller in main.go owns reading/writing the file.
//
// trailerName is config-derived (trailer.Name(cfg, KeyCost), default "Claude-Cost")
// so summing tracks whatever the cost trailer was actually written as; a hard-coded
// name would silently stop summing for any team using [format.rename].
//
// Only lines whose value parses as a number are summed. Everything else is left
// exactly as-is: the "-Models" aggregate (a different trailer name), any other
// trailer, and even a same-named line with a non-numeric value. A run with fewer
// than two numeric matches returns the input unchanged. The summed value keeps the
// greatest decimal precision seen among the inputs, so nothing is rounded away.
func SumDuplicates(lines []string, trailerName string) []string {
	var idxs []int
	var sum float64
	maxPrec := 0
	for i, line := range lines {
		val, ok := costValue(line, trailerName)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			continue // same name but non-numeric value — leave untouched
		}
		idxs = append(idxs, i)
		sum += f
		if p := decimals(val); p > maxPrec {
			maxPrec = p
		}
	}
	if len(idxs) < 2 {
		return lines // nothing to collapse
	}

	summed := fmt.Sprintf("%s: %.*f", trailerName, maxPrec, sum)
	drop := make(map[int]bool, len(idxs)-1)
	for _, j := range idxs[1:] {
		drop[j] = true
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		switch {
		case i == idxs[0]:
			out = append(out, summed)
		case drop[i]:
			// duplicate — folded into the summed line above
		default:
			out = append(out, line)
		}
	}
	return out
}

// costValue returns the trimmed value of a "<trailerName>: value" line, and false
// for anything else. The ':' must follow trailerName exactly, so "Claude-Cost"
// never matches the "Claude-Cost-Models" aggregate.
func costValue(line, trailerName string) (string, bool) {
	rest, ok := strings.CutPrefix(line, trailerName)
	if !ok {
		return "", false
	}
	rest, ok = strings.CutPrefix(rest, ":")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// decimals counts the digits after the decimal point in a numeric string.
func decimals(s string) int {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}
