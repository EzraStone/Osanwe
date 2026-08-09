package gateway

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

const (
	microsPerUnit          = uint64(1_000_000)
	inputTokenPadding      = uint64(1_024)
	maximumPricePerMillion = uint64(1_000_000_000_000) // 1,000,000 currency units.
)

// CostRates holds prices in millionths of a currency unit per one million
// tokens. The command-line interface uses USD, while the arithmetic itself is
// currency-agnostic as long as the budget and every rate use the same unit.
type CostRates struct {
	InputMicrosPerMillion  uint64
	OutputMicrosPerMillion uint64
}

func (r CostRates) validOptional() error {
	if r.InputMicrosPerMillion == 0 && r.OutputMicrosPerMillion == 0 {
		return nil
	}
	if r.InputMicrosPerMillion == 0 || r.OutputMicrosPerMillion == 0 {
		return errors.New("both input and output prices are required")
	}
	if r.InputMicrosPerMillion > maximumPricePerMillion || r.OutputMicrosPerMillion > maximumPricePerMillion {
		return errors.New("a price is outside the supported range")
	}
	return nil
}

func (r CostRates) priced() bool {
	return r.InputMicrosPerMillion > 0 && r.OutputMicrosPerMillion > 0
}

// EstimateCostMicros reserves a conservative price before provider dispatch.
// Each normalized input byte is treated as one input token, plus fixed padding
// for provider message framing. Actual tokenization is provider-specific, so
// this is an operator guardrail rather than a billing oracle.
func EstimateCostMicros(inputBytes, maxOutputTokens int, rates CostRates) (uint64, error) {
	if inputBytes < 1 || maxOutputTokens < 1 {
		return 0, errors.New("gateway: cost estimate requires positive input bytes and output tokens")
	}
	if err := rates.validOptional(); err != nil {
		return 0, fmt.Errorf("gateway: invalid cost rates: %w", err)
	}
	if !rates.priced() {
		return 0, nil
	}
	inputUnits := uint64(inputBytes)
	if inputUnits > math.MaxUint64-inputTokenPadding {
		return 0, errors.New("gateway: cost estimate overflow")
	}
	inputCost, err := ceilMulDiv(inputUnits+inputTokenPadding, rates.InputMicrosPerMillion, microsPerUnit)
	if err != nil {
		return 0, err
	}
	outputCost, err := ceilMulDiv(uint64(maxOutputTokens), rates.OutputMicrosPerMillion, microsPerUnit)
	if err != nil || inputCost > math.MaxUint64-outputCost {
		return 0, errors.New("gateway: cost estimate overflow")
	}
	return inputCost + outputCost, nil
}

func ceilMulDiv(left, right, divisor uint64) (uint64, error) {
	hi, lo := bits.Mul64(left, right)
	if divisor == 0 || hi >= divisor {
		return 0, errors.New("gateway: cost estimate overflow")
	}
	quotient, remainder := bits.Div64(hi, lo, divisor)
	if remainder != 0 {
		if quotient == math.MaxUint64 {
			return 0, errors.New("gateway: cost estimate overflow")
		}
		quotient++
	}
	return quotient, nil
}

// ParseCurrencyMicros converts a positive decimal currency amount to an exact
// integer number of millionths. More than six decimal places are rejected
// instead of silently rounded.
func ParseCurrencyMicros(value string) (uint64, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.Count(value, ".") > 1 {
		return 0, errors.New("amount must be a positive decimal with at most six fractional digits")
	}
	parts := strings.SplitN(value, ".", 2)
	if parts[0] == "" {
		parts[0] = "0"
	}
	if len(parts) == 2 && len(parts[1]) > 6 {
		return 0, errors.New("amount must have at most six fractional digits")
	}
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, errors.New("amount must be a positive decimal")
	}
	fraction := uint64(0)
	if len(parts) == 2 && parts[1] != "" {
		for _, r := range parts[1] {
			if r < '0' || r > '9' {
				return 0, errors.New("amount must be a positive decimal")
			}
		}
		fraction, err = strconv.ParseUint(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10, 64)
		if err != nil {
			return 0, errors.New("amount must be a positive decimal")
		}
	}
	if whole > (math.MaxUint64-fraction)/microsPerUnit {
		return 0, errors.New("amount is too large")
	}
	micros := whole*microsPerUnit + fraction
	if micros == 0 {
		return 0, errors.New("amount must be positive")
	}
	return micros, nil
}
