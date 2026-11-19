// Package stats holds small, dependency-free statistical helpers for e2e qualification —
// currently the Pearson chi-square goodness-of-fit test a weighted-routing suite uses to
// decide whether an observed traffic split matches configured weights. No Kubernetes
// knowledge; pure arithmetic, so it is unit tested without a cluster.
package stats

import "sort"

// ChiSquareCritical holds upper-tail critical values χ²_{α}(df) for the alphas a routing
// qualification needs. The outer key is the degrees of freedom, the inner the alpha.
// α=0.001 values: df=1 → 10.828, df=2 → 13.816.
var chiSquareCritical = map[int]map[float64]float64{
	1: {0.001: 10.828},
	2: {0.001: 13.816},
}

// ChiSquareCritical returns the upper-tail critical value for df at alpha, and whether one
// is tabulated. The qualification only uses the small (df, alpha) pairs above.
func ChiSquareCritical(df int, alpha float64) (float64, bool) {
	byAlpha, ok := chiSquareCritical[df]
	if !ok {
		return 0, false
	}
	v, ok := byAlpha[alpha]
	return v, ok
}

// GOFResult is the outcome of a goodness-of-fit computation.
type GOFResult struct {
	// Statistic is the Pearson χ² statistic.
	Statistic float64
	// DegreesOfFreedom is (number of positive-weight categories) − 1.
	DegreesOfFreedom int
	// N is the total observation count (summed across every category in observed).
	N int
	// Expected maps each positive-weight category to its expected count under the weights.
	Expected map[string]float64
}

// GoodnessOfFit computes the Pearson χ² statistic for observed category counts against the
// expected proportions implied by weights. Only positive-weight categories contribute to
// the statistic and the degrees of freedom; the total N is summed across every category in
// observed (so a category that was disabled but still received traffic inflates N and the
// statistic, as it should). Categories present in weights but absent from observed are
// treated as zero observed.
func GoodnessOfFit(observed map[string]int, weights map[string]int) GOFResult {
	n := 0
	for _, c := range observed {
		n += c
	}

	totalWeight := 0
	active := make([]string, 0, len(weights))
	for cat, w := range weights {
		if w > 0 {
			totalWeight += w
			active = append(active, cat)
		}
	}
	sort.Strings(active)

	expected := make(map[string]float64, len(active))
	statistic := 0.0
	for _, cat := range active {
		exp := float64(n) * float64(weights[cat]) / float64(totalWeight)
		expected[cat] = exp
		if exp > 0 {
			diff := float64(observed[cat]) - exp
			statistic += diff * diff / exp
		}
	}

	df := max(len(active)-1, 0)
	return GOFResult{Statistic: statistic, DegreesOfFreedom: df, N: n, Expected: expected}
}

// FitsDistribution reports whether observed counts fit the weighted distribution at the
// given alpha: the χ² statistic must not exceed the tabulated critical value for its
// degrees of freedom. ok is false when no critical value is tabulated for (df, alpha), so
// a caller can distinguish "does not fit" from "cannot decide".
func FitsDistribution(observed map[string]int, weights map[string]int, alpha float64) (fits, ok bool, result GOFResult) {
	result = GoodnessOfFit(observed, weights)
	critical, have := ChiSquareCritical(result.DegreesOfFreedom, alpha)
	if !have {
		return false, false, result
	}
	return result.Statistic <= critical, true, result
}
