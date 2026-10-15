//go:generate protoc -I . --go_out=. --go_opt=module=github.com/SanishKumar/go-microservices/account --go-grpc_out=. --go-grpc_opt=module=github.com/SanishKumar/go-microservices/account account.proto
package account

import (
	"context"
	"errors"

	"github.com/SanishKumar/go-microservices/account/pb"
	"github.com/SanishKumar/go-microservices/internal/rpcerr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type grpcServer struct {
	pb.UnimplementedAccountServiceServer
	service Service
}

// Register mounts the account service on a gRPC server. Callers own the
// server so they can install interceptors and manage its lifecycle.
func Register(gs *grpc.Server, s Service) {
	pb.RegisterAccountServiceServer(gs, &grpcServer{service: s})
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return rpcerr.Wrap(codes.NotFound, err)
	case errors.Is(err, ErrInvalidName):
		return rpcerr.Wrap(codes.InvalidArgument, err)
	}
	return rpcerr.FromError(err)
}

func (s *grpcServer) PostAccount(ctx context.Context, r *pb.PostAccountRequest) (*pb.PostAccountResponse, error) {
	a, err := s.service.PostAccount(ctx, r.Name)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.PostAccountResponse{Account: &pb.Account{Id: a.ID, Name: a.Name}}, nil
}

func (s *grpcServer) GetAccount(ctx context.Context, r *pb.GetAccountRequest) (*pb.GetAccountResponse, error) {
	a, err := s.service.GetAccount(ctx, r.Id)
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.GetAccountResponse{Account: &pb.Account{Id: a.ID, Name: a.Name}}, nil
}

func (s *grpcServer) GetAccounts(ctx context.Context, r *pb.GetAccountsRequest) (*pb.GetAccountsResponse, error) {
	res, err := s.service.GetAccounts(ctx, r.Skip, r.Take)
	if err != nil {
		return nil, toStatus(err)
	}
	accounts := make([]*pb.Account, 0, len(res))
	for _, a := range res {
		accounts = append(accounts, &pb.Account{Id: a.ID, Name: a.Name})
	}
	return &pb.GetAccountsResponse{Accounts: accounts}, nil
}
