//go:generate go run github.com/99designs/gqlgen generate
package graphql

import (
	"context"
	"errors"
	"net/http"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/order"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"google.golang.org/grpc/status"
)

// Server resolves the public GraphQL API against the backend services.
type Server struct {
	accountClient *account.Client
	catalogClient *catalog.Client
	orderClient   *order.Client
}

func NewServer(accountClient *account.Client, catalogClient *catalog.Client, orderClient *order.Client) *Server {
	return &Server{accountClient, catalogClient, orderClient}
}

func (s *Server) Mutation() MutationResolver {
	return &mutationResolver{server: s}
}

func (s *Server) Query() QueryResolver {
	return &queryResolver{server: s}
}

func (s *Server) Account() AccountResolver {
	return &accountResolver{server: s}
}

func (s *Server) ToExecutableSchema() graphql.ExecutableSchema {
	return NewExecutableSchema(Config{Resolvers: s})
}

// Handler serves the API. Resolver errors carry their gRPC status as
// extensions.code so clients (and the Backdraft agent) can tell a timeout
// from a bad request.
func (s *Server) Handler() http.Handler {
	srv := handler.New(s.ToExecutableSchema())
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.GET{})
	srv.Use(extension.Introspection{})
	srv.SetErrorPresenter(func(ctx context.Context, err error) *gqlerror.Error {
		e := graphql.DefaultErrorPresenter(ctx, err)
		if inner := errors.Unwrap(e); inner == nil {
			return e
		}
		if st, ok := status.FromError(errors.Unwrap(e)); ok {
			e.Message = st.Message()
			if e.Extensions == nil {
				e.Extensions = map[string]any{}
			}
			e.Extensions["code"] = backdraft.CodeName(st.Code())
		}
		return e
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		oc := graphql.GetOperationContext(ctx)
		name := oc.OperationName
		if name == "" && oc.Operation != nil && len(oc.Operation.SelectionSet) > 0 {
			// Anonymous operations are named after their first root field.
			if f, ok := oc.Operation.SelectionSet[0].(*ast.Field); ok {
				name = f.Name
			}
		}
		backdraft.SetOperation(ctx, name)
		return next(ctx)
	})
	srv.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
		resp := next(ctx)
		if resp != nil && len(resp.Errors) > 0 {
			code, _ := resp.Errors[0].Extensions["code"].(string)
			c, ok := backdraft.ParseCode(code)
			if !ok {
				c, _ = backdraft.ParseCode("INTERNAL")
			}
			backdraft.SetFailed(ctx, c)
		}
		return resp
	})
	return withIdempotencyKey(srv)
}

// Routes mounts the API behind the Backdraft agent, plus a playground.
func Routes(s *Server, agent *backdraft.Agent) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/graphql", agent.Middleware(s.Handler()))
	mux.Handle("/playground", playground.Handler("go-microservices", "/graphql"))
	return mux
}

type idempotencyKey struct{}

// withIdempotencyKey carries the client's Idempotency-Key header to the
// mutation resolvers.
func withIdempotencyKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Idempotency-Key"); k != "" {
			r = r.WithContext(context.WithValue(r.Context(), idempotencyKey{}, k))
		}
		next.ServeHTTP(w, r)
	})
}
