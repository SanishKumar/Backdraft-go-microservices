package graphql

import (
	"context"
)

type queryResolver struct {
	server *Server
}

func (r *queryResolver) Accounts(ctx context.Context, pagination *PaginationInput, id *string) ([]*Account, error) {
	if id != nil {
		a, err := r.server.accountClient.GetAccount(ctx, *id)
		if err != nil {
			return nil, err
		}
		return []*Account{{ID: a.ID, Name: a.Name}}, nil
	}

	skip, take := pagination.bounds()
	accountList, err := r.server.accountClient.GetAccounts(ctx, skip, take)
	if err != nil {
		return nil, err
	}
	accounts := make([]*Account, 0, len(accountList))
	for _, a := range accountList {
		accounts = append(accounts, &Account{ID: a.ID, Name: a.Name})
	}
	return accounts, nil
}

func (r *queryResolver) Products(ctx context.Context, pagination *PaginationInput, query *string, id *string) ([]*Product, error) {
	if id != nil {
		p, err := r.server.catalogClient.GetProduct(ctx, *id)
		if err != nil {
			return nil, err
		}
		return []*Product{{ID: p.ID, Name: p.Name, Description: p.Description, Price: p.Price}}, nil
	}

	skip, take := pagination.bounds()
	q := ""
	if query != nil {
		q = *query
	}
	productList, err := r.server.catalogClient.GetProducts(ctx, skip, take, nil, q)
	if err != nil {
		return nil, err
	}
	products := make([]*Product, 0, len(productList))
	for _, p := range productList {
		products = append(products, &Product{ID: p.ID, Name: p.Name, Description: p.Description, Price: p.Price})
	}
	return products, nil
}

func (p *PaginationInput) bounds() (skip, take uint64) {
	skip, take = 0, 100
	if p == nil {
		return
	}
	if p.Skip != nil && *p.Skip > 0 {
		skip = uint64(*p.Skip)
	}
	if p.Take != nil && *p.Take > 0 {
		take = uint64(*p.Take)
	}
	return
}
