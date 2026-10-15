package graphql

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/SanishKumar/go-microservices/order"
	"github.com/segmentio/ksuid"
)

var ErrInvalidParameter = errors.New("invalid parameter")

type mutationResolver struct {
	server *Server
}

func (r *mutationResolver) CreateAccount(ctx context.Context, in AccountInput) (*Account, error) {
	a, err := r.server.accountClient.PostAccount(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	return &Account{ID: a.ID, Name: a.Name}, nil
}

func (r *mutationResolver) CreateProduct(ctx context.Context, in ProductInput) (*Product, error) {
	p, err := r.server.catalogClient.PostProduct(ctx, in.Name, in.Description, in.Price)
	if err != nil {
		return nil, err
	}
	return &Product{ID: p.ID, Name: p.Name, Description: p.Description, Price: p.Price}, nil
}

// CreateOrder places an order under an idempotency key, so every retry of
// it - by the gateway's own client or by a user resubmitting with the same
// Idempotency-Key header - lands on the same order.
func (r *mutationResolver) CreateOrder(ctx context.Context, in OrderInput) (*Order, error) {
	lines := make([]order.Line, 0, len(in.Products))
	for _, p := range in.Products {
		if p.Quantity <= 0 {
			return nil, ErrInvalidParameter
		}
		lines = append(lines, order.Line{ProductID: p.ID, Quantity: uint32(p.Quantity)})
	}
	o, err := r.server.orderClient.PostOrder(ctx, in.AccountID, lines, mutationKey(ctx))
	if err != nil {
		return nil, err
	}
	return toOrder(o), nil
}

// mutationKey scopes the request's Idempotency-Key to this field, so two
// createOrder fields in one request do not collide. Without a header every
// request gets a fresh key: the gateway's retries are still deduplicated.
func mutationKey(ctx context.Context) string {
	k, _ := ctx.Value(idempotencyKey{}).(string)
	if k == "" {
		k = ksuid.New().String()
	}
	if fc := graphql.GetFieldContext(ctx); fc != nil && fc.Field.Alias != "" {
		k += "/" + fc.Field.Alias
	}
	return k
}

func toOrder(o *order.Order) *Order {
	products := make([]*OrderedProduct, 0, len(o.Products))
	for _, p := range o.Products {
		products = append(products, &OrderedProduct{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Price:       p.Price,
			Quantity:    int(p.Quantity),
		})
	}
	return &Order{ID: o.ID, CreatedAt: o.CreatedAt, TotalPrice: o.TotalPrice, Products: products}
}
