package order

import (
	"context"

	"github.com/SanishKumar/go-microservices/order/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn    *grpc.ClientConn
	service pb.OrderServiceClient
}

func NewClient(url string, opts ...grpc.DialOption) (*Client, error) {
	opts = append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)
	conn, err := grpc.NewClient(url, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn, pb.NewOrderServiceClient(conn)}, nil
}

func (c *Client) Close() {
	c.conn.Close()
}

func fromProto(op *pb.Order) Order {
	o := Order{
		ID:         op.Id,
		AccountID:  op.AccountId,
		TotalPrice: op.TotalPrice,
		CreatedAt:  op.CreatedAt.AsTime(),
		Products:   make([]OrderedProduct, 0, len(op.Products)),
	}
	for _, p := range op.Products {
		o.Products = append(o.Products, OrderedProduct{
			ID:          p.Id,
			Name:        p.Name,
			Description: p.Description,
			Price:       p.Price,
			Quantity:    p.Quantity,
		})
	}
	return o
}

// PostOrder places an order. Reusing idempotencyKey for the same account
// returns the original order instead of creating another one.
func (c *Client) PostOrder(ctx context.Context, accountID string, lines []Line, idempotencyKey string) (*Order, error) {
	req := &pb.PostOrderRequest{AccountId: accountID, IdempotencyKey: idempotencyKey}
	for _, l := range lines {
		req.Products = append(req.Products, &pb.PostOrderRequest_OrderProduct{
			ProductId: l.ProductID,
			Quantity:  l.Quantity,
		})
	}
	r, err := c.service.PostOrder(ctx, req)
	if err != nil {
		return nil, err
	}
	o := fromProto(r.Order)
	return &o, nil
}

func (c *Client) GetOrdersForAccount(ctx context.Context, accountID string) ([]Order, error) {
	r, err := c.service.GetOrdersForAccount(ctx, &pb.GetOrdersForAccountRequest{AccountId: accountID})
	if err != nil {
		return nil, err
	}
	orders := make([]Order, 0, len(r.Orders))
	for _, op := range r.Orders {
		orders = append(orders, fromProto(op))
	}
	return orders, nil
}
