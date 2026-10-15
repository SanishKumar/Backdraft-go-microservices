package graphql

import (
	"context"
)

type accountResolver struct {
	server *Server
}

func (r *accountResolver) Orders(ctx context.Context, obj *Account) ([]*Order, error) {
	orderList, err := r.server.orderClient.GetOrdersForAccount(ctx, obj.ID)
	if err != nil {
		return nil, err
	}
	orders := make([]*Order, 0, len(orderList))
	for i := range orderList {
		orders = append(orders, toOrder(&orderList[i]))
	}
	return orders, nil
}
