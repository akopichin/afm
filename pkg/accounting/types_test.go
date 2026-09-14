package accounting

import "testing"

func TestTokensDerived(t *testing.T) {
	tk := Tokens{UncachedInput: 2, CacheRead: 15940, CacheWrite1h: 18551, Output: 345, ReasoningOutput: 0}
	if got := tk.InputTotal(); got != 2+15940+18551 {
		t.Fatalf("InputTotal = %d", got)
	}
	if got := tk.CacheWriteTotal(); got != 18551 {
		t.Fatalf("CacheWriteTotal = %d", got)
	}
	// reasoning is a subset of output and must NOT be added on top of Total
	if got := tk.Total(); got != tk.InputTotal()+tk.Output {
		t.Fatalf("Total = %d", got)
	}
}

func TestTokensAdd(t *testing.T) {
	a := Tokens{UncachedInput: 1, Output: 2}
	b := Tokens{UncachedInput: 3, CacheRead: 4, Output: 5}
	sum := a.Add(b)
	if sum.UncachedInput != 4 || sum.CacheRead != 4 || sum.Output != 7 {
		t.Fatalf("Add = %+v", sum)
	}
}
