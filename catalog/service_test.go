package catalog

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestPriceFitsOrderStorage(t *testing.T) {
	s := NewService(NewMemoryRepository())
	for _, price := range []float64{-1, math.NaN(), math.Inf(1), math.MaxFloat64, 10_000_000_000} {
		if _, err := s.PostProduct(context.Background(), "Lamp", "", price); !errors.Is(err, ErrInvalidProduct) {
			t.Errorf("accepted price %v: %v", price, err)
		}
	}
	p, err := s.PostProduct(context.Background(), "Lamp", "", 9_999_999_999.99)
	if err != nil || p.Price != 9_999_999_999.99 {
		t.Fatalf("valid boundary price: %v, %v", p, err)
	}
}
