package order

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/catalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRejectsQuantitiesAndTotalsOutsideStorageRange(t *testing.T) {
	for _, lines := range [][]Line{
		{{"lamp", math.MaxUint32}},
		{{"lamp", math.MaxInt32}, {"lamp", 1}},
	} {
		s, _ := newTestService()
		if _, _, err := s.PostOrder(context.Background(), "acct", lines, ""); !errors.Is(err, ErrInvalidOrder) {
			t.Errorf("accepted invalid quantity: %v", err)
		}
	}
	s, products := newTestService()
	products["lamp"] = catalog.Product{ID: "lamp", Price: 9_999_999_999.99}
	if _, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 2}}, ""); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("accepted overflowing total: %v", err)
	}
	products["lamp"] = catalog.Product{ID: "lamp", Price: math.NaN()}
	if _, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}}, ""); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("accepted nonfinite catalog price: %v", err)
	}
}

func TestMemoryOrderHistoryCannotBeMutatedByCallers(t *testing.T) {
	s, _ := newTestService()
	o, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}}, "key")
	if err != nil {
		t.Fatal(err)
	}
	o.Products[0].Price = 99
	history, err := s.GetOrdersForAccount(context.Background(), "acct")
	if err != nil || history[0].Products[0].Price != 0.1 {
		t.Fatalf("returned order changed storage: %v, %v", history, err)
	}
	history[0].Products[0].Price = 123
	replay, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}}, "key")
	if err != nil || replay.Products[0].Price != 0.1 {
		t.Fatalf("history changed replay: %v, %v", replay, err)
	}
}

type fakeAccounts struct{ err error }

func (f fakeAccounts) GetAccount(_ context.Context, id string) (*account.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &account.Account{ID: id, Name: "Ada"}, nil
}

type fakeCatalog map[string]catalog.Product

func (f fakeCatalog) GetProducts(_ context.Context, _, _ uint64, ids []string, _ string) ([]catalog.Product, error) {
	var out []catalog.Product
	for _, id := range ids {
		if p, ok := f[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func newTestService() (Service, fakeCatalog) {
	products := fakeCatalog{
		"lamp": {ID: "lamp", Name: "Lamp", Price: 0.1},
		"mug":  {ID: "mug", Name: "Mug", Price: 0.2},
	}
	return NewService(NewMemoryRepository(), fakeAccounts{}, products), products
}

func TestRepeatedProductsAreMerged(t *testing.T) {
	s, _ := newTestService()
	o, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}, {"mug", 1}, {"lamp", 2}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Products) != 2 {
		t.Fatalf("got %d lines, want 2 merged lines", len(o.Products))
	}
	for _, p := range o.Products {
		if p.ID == "lamp" && p.Quantity != 3 {
			t.Errorf("lamp quantity = %d, want 3", p.Quantity)
		}
	}
	// 3 x 0.10 + 0.20 in cents, not 0.5000000000000001.
	if o.TotalPrice != 0.5 {
		t.Errorf("total = %v, want 0.5", o.TotalPrice)
	}
}

func TestUnknownProductsAreRejectedNotDropped(t *testing.T) {
	s, _ := newTestService()
	_, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}, {"ghost", 1}}, "")
	if !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("err = %v, want ErrInvalidOrder", err)
	}
	if _, _, err := s.PostOrder(context.Background(), "acct", nil, ""); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("empty order: err = %v", err)
	}
	if _, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 0}}, ""); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("zero quantity: err = %v", err)
	}
}

func TestOrderHistoryKeepsThePricePaid(t *testing.T) {
	s, products := newTestService()
	if _, _, err := s.PostOrder(context.Background(), "acct", []Line{{"lamp", 1}}, ""); err != nil {
		t.Fatal(err)
	}
	products["lamp"] = catalog.Product{ID: "lamp", Name: "Lamp (new)", Price: 99}
	orders, err := s.GetOrdersForAccount(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	if p := orders[0].Products[0]; p.Price != 0.1 || p.Name != "Lamp" {
		t.Fatalf("history shows %q at %v, want the original Lamp at 0.1", p.Name, p.Price)
	}
}

func TestIdempotencyKeyReplaysTheOriginalOrder(t *testing.T) {
	s, _ := newTestService()
	first, replayed, err := s.PostOrder(context.Background(), "acct", []Line{{"mug", 1}}, "key-1")
	if err != nil || replayed {
		t.Fatalf("first: replayed=%v err=%v", replayed, err)
	}
	again, replayed, err := s.PostOrder(context.Background(), "acct", []Line{{"mug", 1}}, "key-1")
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("retry: id %s vs %s, replayed=%v err=%v", again.ID, first.ID, replayed, err)
	}
	orders, _ := s.GetOrdersForAccount(context.Background(), "acct")
	if len(orders) != 1 {
		t.Fatalf("%d orders stored, want 1", len(orders))
	}
	// Keys are scoped per account.
	if _, replayed, _ := s.PostOrder(context.Background(), "other", []Line{{"mug", 1}}, "key-1"); replayed {
		t.Fatal("another account's key replayed")
	}
}

// Downstream failures keep their status so callers can tell a transient
// failure (retry) from a missing account (do not).
func TestAccountErrorsKeepTheirMeaning(t *testing.T) {
	products := fakeCatalog{"mug": {ID: "mug", Price: 1}}
	missing := NewService(NewMemoryRepository(), fakeAccounts{status.Error(codes.NotFound, "no")}, products)
	if _, _, err := missing.PostOrder(context.Background(), "acct", []Line{{"mug", 1}}, ""); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("not found: err = %v", err)
	}
	down := NewService(NewMemoryRepository(), fakeAccounts{status.Error(codes.Unavailable, "down")}, products)
	_, _, err := down.PostOrder(context.Background(), "acct", []Line{{"mug", 1}}, "")
	if status.Code(toStatus(err)) != codes.Unavailable {
		t.Fatalf("unavailable became %v", status.Code(toStatus(err)))
	}
}
