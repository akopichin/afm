package main

import (
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/config"
)

// TestServerAccountingProvider covers the display gate on top of the existing
// acct/acctErr matrix: display=false must return nil (the dashboard omits every
// cost field) regardless of the ledger, while display=true keeps the previous
// behavior — the live *Store, StaticUnavailable on a hard open error, or nil.
func TestServerAccountingProvider(t *testing.T) {
	acct, err := accounting.Open(t.TempDir(), accounting.NewResolver(config.Config{}.Pricing))
	if err != nil {
		t.Fatalf("open accounting: %v", err)
	}
	t.Cleanup(func() { _ = acct.Close() })

	openErr := errors.New("permission denied")

	t.Run("display off returns nil even with a live store", func(t *testing.T) {
		if got := serverAccountingProvider(false, acct, nil); got != nil {
			t.Fatalf("display=false: got %v, want nil", got)
		}
	})
	t.Run("display off returns nil even with an open error", func(t *testing.T) {
		if got := serverAccountingProvider(false, nil, openErr); got != nil {
			t.Fatalf("display=false: got %v, want nil", got)
		}
	})
	t.Run("display on returns the live store", func(t *testing.T) {
		if got := serverAccountingProvider(true, acct, nil); got != accounting.CostProvider(acct) {
			t.Fatalf("display=true with store: got %v, want the *Store", got)
		}
	})
	t.Run("display on returns StaticUnavailable on open error", func(t *testing.T) {
		got := serverAccountingProvider(true, nil, openErr)
		if got == nil {
			t.Fatal("display=true with open error: got nil, want StaticUnavailable")
		}
		if got == accounting.CostProvider(acct) {
			t.Fatal("display=true with open error: must not be the live store")
		}
	})
	t.Run("display on with neither returns nil", func(t *testing.T) {
		if got := serverAccountingProvider(true, nil, nil); got != nil {
			t.Fatalf("display=true with neither: got %v, want nil", got)
		}
	})
}
