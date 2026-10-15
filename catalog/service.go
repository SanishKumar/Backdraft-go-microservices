package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/segmentio/ksuid"
)

var (
	ErrNotFound       = errors.New("product not found")
	ErrInvalidProduct = errors.New("invalid product")
)

type Service interface {
	PostProduct(ctx context.Context, name, description string, price float64) (*Product, error)
	GetProduct(ctx context.Context, id string) (*Product, error)
	GetProducts(ctx context.Context, skip uint64, take uint64) ([]Product, error)
	GetProductsByIDs(ctx context.Context, ids []string) ([]Product, error)
	SearchProducts(ctx context.Context, query string, skip uint64, take uint64) ([]Product, error)
}

type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
}

type catalogService struct {
	repository Repository
}

func NewService(r Repository) Service {
	return &catalogService{r}
}

func (s *catalogService) PostProduct(ctx context.Context, name, description string, price float64) (*Product, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidProduct)
	}
	if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return nil, fmt.Errorf("%w: price must be a non-negative number", ErrInvalidProduct)
	}
	p := &Product{
		Name:        name,
		Description: description,
		Price:       math.Round(price*100) / 100,
		ID:          ksuid.New().String(),
	}
	if err := s.repository.PutProduct(ctx, *p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *catalogService) GetProduct(ctx context.Context, id string) (*Product, error) {
	return s.repository.GetProductByID(ctx, id)
}

func (s *catalogService) GetProducts(ctx context.Context, skip uint64, take uint64) ([]Product, error) {
	if take > 100 || take == 0 {
		take = 100
	}
	return s.repository.ListProducts(ctx, skip, take)
}

// GetProductsByIDs returns the products that exist, in request order.
// Missing IDs are omitted; callers that need all of them must check.
func (s *catalogService) GetProductsByIDs(ctx context.Context, ids []string) ([]Product, error) {
	return s.repository.ListProductsWithIDs(ctx, ids)
}

func (s *catalogService) SearchProducts(ctx context.Context, query string, skip uint64, take uint64) ([]Product, error) {
	if take > 100 || take == 0 {
		take = 100
	}
	return s.repository.SearchProducts(ctx, query, skip, take)
}
