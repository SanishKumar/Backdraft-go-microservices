package catalog

import (
	"context"

	"github.com/SanishKumar/go-microservices/catalog/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn    *grpc.ClientConn
	service pb.CatalogServiceClient
}

func NewClient(url string, opts ...grpc.DialOption) (*Client, error) {
	opts = append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)
	conn, err := grpc.NewClient(url, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn, pb.NewCatalogServiceClient(conn)}, nil
}

func (c *Client) Close() {
	c.conn.Close()
}

func fromProto(p *pb.Product) Product {
	return Product{ID: p.Id, Name: p.Name, Description: p.Description, Price: p.Price}
}

func (c *Client) PostProduct(ctx context.Context, name, description string, price float64) (*Product, error) {
	r, err := c.service.PostProduct(ctx, &pb.PostProductRequest{
		Name:        name,
		Description: description,
		Price:       price,
	})
	if err != nil {
		return nil, err
	}
	p := fromProto(r.Product)
	return &p, nil
}

func (c *Client) GetProduct(ctx context.Context, id string) (*Product, error) {
	r, err := c.service.GetProduct(ctx, &pb.GetProductRequest{Id: id})
	if err != nil {
		return nil, err
	}
	p := fromProto(r.Product)
	return &p, nil
}

func (c *Client) GetProducts(ctx context.Context, skip uint64, take uint64, ids []string, query string) ([]Product, error) {
	r, err := c.service.GetProducts(ctx, &pb.GetProductsRequest{
		Ids:   ids,
		Skip:  skip,
		Take:  take,
		Query: query,
	})
	if err != nil {
		return nil, err
	}
	products := make([]Product, 0, len(r.Products))
	for _, p := range r.Products {
		products = append(products, fromProto(p))
	}
	return products, nil
}
