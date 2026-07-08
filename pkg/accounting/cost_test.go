package accounting_test

import (
	"math"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
)

// TestDeriveCostSignature — контрактный тест: DeriveCost существует с объявленной
// сигнатурой DeriveCost(int, int, int, config.ModelPricing) float64 и компилируется.
// Нулевые токены — простейший корректный вызов.
func TestDeriveCostSignature(t *testing.T) {
	pricing := config.ModelPricing{InputPerMtok: 1, OutputPerMtok: 1, CachePerMtok: 1}
	if got := accounting.DeriveCost(0, 0, 0, pricing); got != 0 {
		t.Fatalf("zero tokens must yield zero cost, got %v", got)
	}
}

// TestDeriveCostComputesWeightedSum — pricing={3.0,15.0,0.3},
// inputTokens=1_000_000, outputTokens=200_000, cacheTokens=500_000.
// inputCost=3.0, outputCost=3.0, cacheCost=0.15 → costUsd=6.15.
func TestDeriveCostComputesWeightedSum(t *testing.T) {
	pricing := config.ModelPricing{
		InputPerMtok:  3.0,
		OutputPerMtok: 15.0,
		CachePerMtok:  0.3,
	}
	got := accounting.DeriveCost(1_000_000, 200_000, 500_000, pricing)
	const want = 6.15
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("weighted sum: got %v want %v", got, want)
	}
}

// TestDeriveCostZeroTokensReturnsZero — все три поля токенов нулевые → 0.0.
// Защита от регрессии с NaN/делением на ноль; зеркалит реально наблюдённое
// is_error:true result-событие с all-zero usage.
func TestDeriveCostZeroTokensReturnsZero(t *testing.T) {
	pricing := config.ModelPricing{InputPerMtok: 3.0, OutputPerMtok: 15.0, CachePerMtok: 0.3}
	got := accounting.DeriveCost(0, 0, 0, pricing)
	if got != 0.0 {
		t.Errorf("zero tokens: got %v want 0.0", got)
	}
}
