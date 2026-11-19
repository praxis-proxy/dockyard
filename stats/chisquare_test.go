package stats

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestGoodnessOfFitEvenSplit(t *testing.T) {
	// 50:50, n=500, expected 250/250. Observed 240/260 → χ² = 0.4+0.4 = 0.8, df=1.
	r := GoodnessOfFit(map[string]int{"a": 240, "b": 260}, map[string]int{"a": 50, "b": 50})
	if !approx(r.Statistic, 0.8) {
		t.Errorf("statistic = %v, want 0.8", r.Statistic)
	}
	if r.DegreesOfFreedom != 1 {
		t.Errorf("df = %d, want 1", r.DegreesOfFreedom)
	}
	if r.N != 500 {
		t.Errorf("N = %d, want 500", r.N)
	}
	if !approx(r.Expected["a"], 250) || !approx(r.Expected["b"], 250) {
		t.Errorf("expected = %v, want 250/250", r.Expected)
	}
}

func TestGoodnessOfFitPerfectSplitIsZero(t *testing.T) {
	r := GoodnessOfFit(map[string]int{"a": 250, "b": 250}, map[string]int{"a": 50, "b": 50})
	if !approx(r.Statistic, 0) {
		t.Errorf("perfect split statistic = %v, want 0", r.Statistic)
	}
}

func TestGoodnessOfFitThreeWayUnequal(t *testing.T) {
	// 50:25:25, n=500, expected 250/125/125. Observed 200/150/150 → 10+5+5 = 20, df=2.
	r := GoodnessOfFit(map[string]int{"a": 200, "b": 150, "c": 150}, map[string]int{"a": 50, "b": 25, "c": 25})
	if !approx(r.Statistic, 20) {
		t.Errorf("statistic = %v, want 20", r.Statistic)
	}
	if r.DegreesOfFreedom != 2 {
		t.Errorf("df = %d, want 2", r.DegreesOfFreedom)
	}
}

func TestGoodnessOfFitIgnoresZeroWeightCategoriesInDFButCountsThemInN(t *testing.T) {
	// c is disabled; its 10 requests inflate n=500 but only a,b contribute terms.
	r := GoodnessOfFit(map[string]int{"a": 240, "b": 250, "c": 10}, map[string]int{"a": 50, "b": 50, "c": 0})
	if r.DegreesOfFreedom != 1 {
		t.Errorf("df = %d, want 1 (only active categories)", r.DegreesOfFreedom)
	}
	if r.N != 500 {
		t.Errorf("N = %d, want 500 (includes disabled traffic)", r.N)
	}
	// expected 250/250 → (240-250)²/250 + (250-250)²/250 = 0.4
	if !approx(r.Statistic, 0.4) {
		t.Errorf("statistic = %v, want 0.4", r.Statistic)
	}
}

func TestChiSquareCritical(t *testing.T) {
	if v, ok := ChiSquareCritical(1, 0.001); !ok || !approx(v, 10.828) {
		t.Errorf("critical(1,0.001) = %v,%v want 10.828,true", v, ok)
	}
	if v, ok := ChiSquareCritical(2, 0.001); !ok || !approx(v, 13.816) {
		t.Errorf("critical(2,0.001) = %v,%v want 13.816,true", v, ok)
	}
	if _, ok := ChiSquareCritical(3, 0.001); ok {
		t.Error("critical(3,0.001) should be untabulated")
	}
	if _, ok := ChiSquareCritical(1, 0.05); ok {
		t.Error("critical(1,0.05) should be untabulated")
	}
}

func TestFitsDistribution(t *testing.T) {
	alpha := 0.001

	// 50:50 with a plausible split fits (χ²=0.8 ≤ 10.828).
	if fits, ok, _ := FitsDistribution(map[string]int{"a": 240, "b": 260}, map[string]int{"a": 50, "b": 50}, alpha); !ok || !fits {
		t.Errorf("plausible 50:50 split should fit (ok=%v fits=%v)", ok, fits)
	}
	// A wildly skewed split does not fit (χ²=180 > 10.828).
	if fits, ok, r := FitsDistribution(map[string]int{"a": 400, "b": 100}, map[string]int{"a": 50, "b": 50}, alpha); !ok || fits {
		t.Errorf("400:100 should not fit 50:50 (ok=%v fits=%v χ²=%v)", ok, fits, r.Statistic)
	}
	// canary 95:5, 470/30 fits (χ²≈1.05).
	if fits, ok, _ := FitsDistribution(map[string]int{"a": 470, "b": 30}, map[string]int{"a": 95, "b": 5}, alpha); !ok || !fits {
		t.Errorf("canary 470:30 should fit 95:5 (ok=%v fits=%v)", ok, fits)
	}
	// canary 95:5, 450/50 does not fit (χ²≈26.3).
	if fits, ok, _ := FitsDistribution(map[string]int{"a": 450, "b": 50}, map[string]int{"a": 95, "b": 5}, alpha); !ok || fits {
		t.Errorf("450:50 should not fit 95:5 (ok=%v fits=%v)", ok, fits)
	}
	// df=2 untabulated at a different alpha → cannot decide.
	if _, ok, _ := FitsDistribution(map[string]int{"a": 200, "b": 150, "c": 150}, map[string]int{"a": 50, "b": 25, "c": 25}, 0.05); ok {
		t.Error("α=0.05 should be undecidable (no tabulated critical value)")
	}
}
