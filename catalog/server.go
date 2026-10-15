//go:generate protoc -I . --go_out=. --go_opt=module=github.com/SanishKumar/go-microservices/catalog --go-grpc_out=. --go-grpc_opt=module=github.com/SanishKumar/go-microservices/catalog catalog.proto
package catalog

import (
	"context"
	"errors"

	"github.com/SanishKumar/go-microservices/catalog/pb"
	"github.com/SanishKumar/go-microservices/internal/rpcerr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type grpcServer struct {
	pb.UnimplementedCatalogServiceServer
	service Service
}

// Register mounts the catalog service on a gRPC server.
func Register(gs *grpc.Server, s Service) {
	pb.RegisterCatalogServiceServer(gs, &grpcServer{service: s})
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return rpcerr.Wrap(codes.NotFound, err)
	case errors.Is(err, ErrInvalidProduct):
		return rpcerr.Wrap(codes.InvalidArgument, err)
	}
	return rpcerr.FromError(err)
}

func toProto(p Product) *pb.Product {
	return &pb.Product{Id: p.ID, Name: p.Name, Description: p.Description, Price: p.Price}
}

func (s *grpcServer) PostProduct(ctx context.Context, r *pb.PostProductRequest) (*pb.PostProductResponse, error) {
	p, err := s.service.PostProduct(ctx, r.Name, r.Description, r.Price)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.PostProductResponse{Product: toProto(*p)}, nil
}

func (s *grpcServer) GetProduct(ctx context.Context, r *pb.GetProductRequest) (*pb.GetProductResponse, error) {
	p, err := s.service.GetProduct(ctx, r.Id)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetProductResponse{Product: toProto(*p)}, nil
}

func (s *grpcServer) GetProducts(ctx context.Context, r *pb.GetProductsRequest) (*pb.GetProductsResponse, error) {
	var res []Product
	var err error
	switch {
	case r.Query != "":
		res, err = s.service.SearchProducts(ctx, r.Query, r.Skip, r.Take)
	case len(r.Ids) != 0:
		res, err = s.service.GetProductsByIDs(ctx, r.Ids)
	default:
		res, err = s.service.GetProducts(ctx, r.Skip, r.Take)
	}
	if err != nil {
		return nil, toStatus(err)
	}
	products := make([]*pb.Product, 0, len(res))
	for _, p := range res {
		products = append(products, toProto(p))
	}
	return &pb.GetProductsResponse{Products: products}, nil
}
