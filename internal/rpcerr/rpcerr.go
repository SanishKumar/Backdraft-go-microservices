// Package rpcerr maps Go errors onto gRPC status codes.
//
// The code a server returns decides whether callers retry, so it matters:
// a downstream timeout reported as codes.Unknown is never retried, while a
// missing record reported as codes.Unavailable is retried until the caller
// gives up.
package rpcerr

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Wrap returns err as a status error carrying code c.
func Wrap(c codes.Code, err error) error {
	return status.Error(c, err.Error())
}

// FromError keeps status errors (including those returned by downstream
// calls) intact, translates context errors, and reports everything else as
// codes.Internal.
func FromError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
