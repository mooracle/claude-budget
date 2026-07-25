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
// one summed line, placed where the LAST duplicate was, dropping the rest. Lines
// in, lines out — no I/O — so the caller in main.go owns reading/writing the file.
//
// Last, not first, because git only treats the message's final paragraph as
// trailers. A squash interleaves messages and trailers:
//
//	first↵↵Claude-Cost: 0.10↵↵second↵↵Claude-Cost: 0.20
//
// folding to the first position leaves "Claude-Cost: 0.30" stranded mid-message,
// where `git interpret-trailers --parse` and %(trailers) return nothing and the
// line degrades to ordinary body text. Folding to the last keeps it in the final
// paragraph, so it stays a real trailer.
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
	keep := idxs[len(idxs)-1] // last, so the fold stays in the trailer paragraph
	drop := make(map[int]bool, len(idxs)-1)
	for _, j := range idxs[:len(idxs)-1] {
		drop[j] = true
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		switch {
		case i == keep:
			out = append(out, summed)
		case drop[i]:
			// earlier duplicate — folded into the summed line below
		default:
			out = append(out, line)
		}
	}
	return out
}

// SumModelDuplicates collapses repeated per-model trailers — the "-Models"
// aggregates, whose value is a "model=value,model=value" list rather than a bare
// number — into a single line, summing each model across the duplicates.
//
// Same situation as SumDuplicates: git concatenates messages on a squash, and an
// amend that hands the previous message back to the hook re-appends a block, so
// the same trailer name can appear twice. Summing per model keeps the aggregate
// consistent with the folded scalar trailer next to it.
//
// The merged line lands at the last duplicate, for the trailer-paragraph reason
// given on SumDuplicates. Models keep first-appearance order, so output is
// stable. Each model's value keeps the greatest decimal precision seen for it,
// so token counts stay integers while costs keep their decimals. If any duplicate line fails to parse as a
// model list, the input is returned unchanged rather than guessed at.
func SumModelDuplicates(lines []string, trailerName string) []string {
	var idxs []int
	order := []string{}
	sums := map[string]float64{}
	precs := map[string]int{}

	for i, line := range lines {
		val, ok := costValue(line, trailerName)
		if !ok {
			continue
		}
		pairs, ok := parseModelList(val)
		if !ok {
			return lines // not a model list — leave everything alone
		}
		idxs = append(idxs, i)
		for _, p := range pairs {
			if _, seen := sums[p.model]; !seen {
				order = append(order, p.model)
			}
			sums[p.model] += p.value
			if p.prec > precs[p.model] {
				precs[p.model] = p.prec
			}
		}
	}
	if len(idxs) < 2 {
		return lines
	}

	parts := make([]string, 0, len(order))
	for _, m := range order {
		parts = append(parts, fmt.Sprintf("%s=%.*f", m, precs[m], sums[m]))
	}
	merged := fmt.Sprintf("%s: %s", trailerName, strings.Join(parts, ","))

	keep := idxs[len(idxs)-1] // last, so the fold stays in the trailer paragraph
	drop := make(map[int]bool, len(idxs)-1)
	for _, j := range idxs[:len(idxs)-1] {
		drop[j] = true
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		switch {
		case i == keep:
			out = append(out, merged)
		case drop[i]:
			// earlier duplicate — folded into the merged line below
		default:
			out = append(out, line)
		}
	}
	return out
}

type modelPair struct {
	model string
	value float64
	prec  int
}

// parseModelList splits "a=1.5,b=2" into pairs. It reports false for anything
// that isn't a non-empty list of model=<number> entries.
func parseModelList(val string) ([]modelPair, bool) {
	if strings.TrimSpace(val) == "" {
		return nil, false
	}
	fields := strings.Split(val, ",")
	pairs := make([]modelPair, 0, len(fields))
	for _, f := range fields {
		name, num, ok := strings.Cut(strings.TrimSpace(f), "=")
		if !ok || name == "" {
			return nil, false
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return nil, false
		}
		pairs = append(pairs, modelPair{model: name, value: v, prec: decimals(num)})
	}
	return pairs, true
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
