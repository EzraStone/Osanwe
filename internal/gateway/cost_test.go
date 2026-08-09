package gateway

import (
	"math"
	"testing"
)

func TestParseCurrencyMicrosIsExact(t *testing.T) {
	tests := map[string]uint64{
		"1": 1_000_000, "1.25": 1_250_000, ".5": 500_000, "0.000001": 1,
	}
	for input, want := range tests {
		got, err := ParseCurrencyMicros(input)
		if err != nil || got != want {
			t.Errorf("ParseCurrencyMicros(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1", "1.0000001", "1e2", " 1", "1." + string([]byte{0xff})} {
		if got, err := ParseCurrencyMicros(input); err == nil {
			t.Errorf("ParseCurrencyMicros(%q) = %d, want error", input, got)
		}
	}
}

func TestEstimateCostMicrosRoundsEachSideUp(t *testing.T) {
	// $3/M input and $15/M output. Input reserves 1024 framing tokens plus
	// the normalized byte length, then each price component rounds upward.
	got, err := EstimateCostMicros(100, 10, CostRates{
		InputMicrosPerMillion: 3_000_000, OutputMicrosPerMillion: 15_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(3_372 + 150); got != want {
		t.Fatalf("cost = %d micros, want %d", got, want)
	}

	got, err = EstimateCostMicros(1, 1, CostRates{InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1})
	if err != nil || got != 2 {
		t.Fatalf("small cost = %d, %v; want two rounded micros", got, err)
	}
}

func TestEstimateCostMicrosAllowsAnExplicitlyUnpricedGateway(t *testing.T) {
	got, err := EstimateCostMicros(100, 100, CostRates{})
	if err != nil || got != 0 {
		t.Fatalf("cost = %d, %v; want disabled", got, err)
	}
}

func TestCostValidationAndArithmeticFailClosed(t *testing.T) {
	if _, err := EstimateCostMicros(1, 1, CostRates{InputMicrosPerMillion: 1}); err == nil {
		t.Fatal("accepted only one side of the price")
	}
	if _, err := ceilMulDiv(math.MaxUint64, math.MaxUint64, 1); err == nil {
		t.Fatal("accepted overflowing multiplication")
	}
}
