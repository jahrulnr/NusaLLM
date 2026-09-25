package valueobject

import "testing"

func TestNewUsage(t *testing.T) {
	t.Run("accepts valid counters", func(t *testing.T) {
		u, err := NewUsage(100, 900, 50, 200)
		if err != nil {
			t.Fatalf("NewUsage: %v", err)
		}
		if u.InputTokens() != 100 || u.CacheReadTokens() != 900 ||
			u.CacheWriteTokens() != 50 || u.OutputTokens() != 200 {
			t.Fatalf("unexpected counters: %+v", u)
		}
	})

	t.Run("rejects negatives", func(t *testing.T) {
		for i, args := range [][4]int{
			{-1, 0, 0, 0}, {0, -1, 0, 0}, {0, 0, -1, 0}, {0, 0, 0, -1},
		} {
			if _, err := NewUsage(args[0], args[1], args[2], args[3]); err == nil {
				t.Fatalf("case %d: expected error for %v", i, args)
			}
		}
	})
}

// NewUsageFromTotal is the adapter entry point for providers reporting an
// inclusive input total with the cached subset nested inside (OpenAI,
// Gemini). The uncached portion must be derived, never double-counted.
func TestNewUsageFromTotal(t *testing.T) {
	t.Run("derives uncached input", func(t *testing.T) {
		// Provider reports input 1000 of which 900 cached.
		u, err := NewUsageFromTotal(1000, 900, 0, 200)
		if err != nil {
			t.Fatalf("NewUsageFromTotal: %v", err)
		}
		if u.InputTokens() != 100 {
			t.Fatalf("uncached input = %d, want 100", u.InputTokens())
		}
		if u.TotalInput() != 1000 {
			t.Fatalf("TotalInput = %d, want 1000 (no double count)", u.TotalInput())
		}
	})

	t.Run("rejects cache counters exceeding total", func(t *testing.T) {
		if _, err := NewUsageFromTotal(100, 900, 300, 0); err == nil {
			t.Fatal("expected error when cache counters exceed total input")
		}
	})
}

func TestUsageTotals(t *testing.T) {
	u, err := NewUsage(100, 900, 50, 200)
	if err != nil {
		t.Fatal(err)
	}
	if u.TotalInput() != 1050 {
		t.Fatalf("TotalInput = %d, want 1050", u.TotalInput())
	}
	if u.Total() != 1250 {
		t.Fatalf("Total = %d, want 1250", u.Total())
	}
}

func TestUsageAdd(t *testing.T) {
	a, _ := NewUsage(100, 900, 50, 200)
	b, _ := NewUsage(10, 20, 30, 40)
	sum := a.Add(b)
	if sum.InputTokens() != 110 || sum.CacheReadTokens() != 920 ||
		sum.CacheWriteTokens() != 80 || sum.OutputTokens() != 240 {
		t.Fatalf("Add produced %+v", sum)
	}
}

func BenchmarkUsageAdd(b *testing.B) {
	u, _ := NewUsage(100, 900, 50, 200)
	o, _ := NewUsage(10, 20, 30, 40)
	for b.Loop() {
		_ = u.Add(o)
	}
}
