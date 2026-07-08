package accounting

import "github.com/akopichin/afm/pkg/config"

// tokensPerMillion — ставка ModelPricing задана за миллион токенов (per-Mtok).
const tokensPerMillion = 1_000_000

// DeriveCost переводит потреблённые токены в стоимость в USD по ставкам pricing.
//
// Вызывающая сторона уже получила pricing через PricingConfig.GetModelPricing —
// эта функция не проверяет отсутствие цены, она только считает по трём ставкам
// (входные, выходные и единая кэш-ставка за миллион токенов).
func DeriveCost(inputTokens int, outputTokens int, cacheTokens int, pricing config.ModelPricing) float64 {
	inputCost := float64(inputTokens) * pricing.InputPerMtok / tokensPerMillion
	outputCost := float64(outputTokens) * pricing.OutputPerMtok / tokensPerMillion
	cacheCost := float64(cacheTokens) * pricing.CachePerMtok / tokensPerMillion
	return inputCost + outputCost + cacheCost
}
