//go:generate protoc -I . --go_out=. --go_opt=module=github.com/SanishKumar/go-microservices/order --go-grpc_out=. --go-grpc_opt=module=github.com/SanishKumar/go-microservices/order order.proto
package order

import (
	"context"
	"errors"

	"github.com/SanishKumar/go-microservices/internal/rpcerr"
	"github.com/SanishKumar/go-microservices/order/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ReplayHeader is set on PostOrder responses that were answered from an
// earlier request with the same idempotency key.
const ReplayHeader = "idempotent-replayed"

type grpcServer struct {
	pb.UnimplementedOrderServiceServer
	service Service
}

// Register mounts the order service on a gRPC server.
func Register(gs *grpc.Server, s Service) {
	pb.RegisterOrderServiceServer(gs, &grpcServer{service: s})
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrInvalidOrder):
		return rpcerr.Wrap(codes.InvalidArgument, err)
	case errors.Is(err, ErrAccountNotFound):
		return rpcerr.Wrap(codes.FailedPrecondition, err)
	case errors.Is(err, ErrNotFound):
		return rpcerr.Wrap(codes.NotFound, err)
	}
	return rpcerr.FromError(err)
}

func toProto(o *Order) *pb.Order {
	op := &pb.Order{
		Id:         o.ID,
		AccountId:  o.AccountID,
		TotalPrice: o.TotalPrice,
		CreatedAt:  timestamppb.New(o.CreatedAt),
		Products:   make([]*pb.Order_OrderProduct, 0, len(o.Products)),
	}
	for _, p := range o.Products {
		op.Products = append(op.Products, &pb.Order_OrderProduct{
			Id:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Price:       p.Price,
			Quantity:    p.Quantity,
		})
	}
	return op
}

func (s *grpcServer) PostOrder(ctx context.Context, r *pb.PostOrderRequest) (*pb.PostOrderResponse, error) {
	lines := make([]Line, 0, len(r.Products))
	for _, p := range r.Products {
		lines = append(lines, Line{ProductID: p.ProductId, Quantity: p.Quantity})
	}
	o, replayed, err := s.service.PostOrder(ctx, r.AccountId, lines, r.IdempotencyKey)
	if err != nil {
		return nil, toStatus(err)
	}
	if replayed {
		_ = grpc.SetHeader(ctx, metadata.Pairs(ReplayHeader, "true"))
	}
	return &pb.PostOrderResponse{Order: toProto(o)}, nil
}

func (s *grpcServer) GetOrdersForAccount(ctx context.Context, r *pb.GetOrdersForAccountRequest) (*pb.GetOrdersForAccountResponse, error) {
	orders, err := s.service.GetOrdersForAccount(ctx, r.AccountId)
	if err != nil {
		return nil, toStatus(err)
	}
	res := &pb.GetOrdersForAccountResponse{Orders: make([]*pb.Order, 0, len(orders))}
	for i := range orders {
		res.Orders = append(res.Orders, toProto(&orders[i]))
	}
	return res, nil
}
