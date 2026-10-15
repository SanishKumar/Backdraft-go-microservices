// Package lab runs the reference system and the Backdraft control plane
// in one process with in-memory stores.
//
// Every service still gets its own gRPC server on its own port, its own
// agent and its own control-plane session, so every call between them is a
// real network round trip through the real interceptors.
package lab

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/backdraft"
	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/tower"
	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/graphql"
	"github.com/SanishKumar/go-microservices/internal/reference"
	"github.com/SanishKumar/go-microservices/internal/serve"
	"github.com/SanishKumar/go-microservices/order"
	"google.golang.org/grpc"
)

// Options configures a lab.
type Options struct {
	// HTTPAddr serves the control plane's API and UI.
	HTTPAddr string
	// GatewayAddr serves the GraphQL gateway.
	GatewayAddr string
	// UIDir is a built web UI; skipped if it does not exist.
	UIDir string
	// RPS is the load generator's initial rate.
	RPS float64
}

// Lab is a running reference system.
type Lab struct {
	Tower   *tower.Tower
	HTTP    *http.Server
	API     string // base URL of the control plane API
	Gateway string // GraphQL endpoint
}

func listen(addr string) (net.Listener, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	return net.Listen("tcp", addr)
}

// Start boots every service and the control plane. Everything stops when
// ctx ends.
func Start(ctx context.Context, opts Options) (*Lab, error) {
	if opts.UIDir != "" {
		if _, err := os.Stat(filepath.Join(opts.UIDir, "index.html")); err != nil {
			log.Printf("lab: no UI build at %s (run `npm run build` in web/, or use the Vite dev server)", opts.UIDir)
			opts.UIDir = ""
		}
	}
	gatewayLis, err := listen(opts.GatewayAddr)
	if err != nil {
		return nil, err
	}
	httpLis, err := listen(opts.HTTPAddr)
	if err != nil {
		return nil, err
	}

	// Control plane first, so agents connect straight away.
	towerLis, err := listen("")
	if err != nil {
		return nil, err
	}
	towerAddr := towerLis.Addr().String()
	t := tower.New(tower.Options{
		Entry:       reference.Users,
		LoadTarget:  fmt.Sprintf("http://%s/graphql", gatewayLis.Addr()),
		EntryPolicy: reference.UsersPolicy(),
		Fallback:    reference.SimModel(reference.System()),
		InitialRPS:  opts.RPS,
		UIDir:       opts.UIDir,
	})
	towerGRPC := grpc.NewServer()
	pb.RegisterControlPlaneServer(towerGRPC, t)
	go serve.GRPC(ctx, "backdraft", towerLis, towerGRPC)

	agent := func(name string, cfg backdraft.ServiceConfig) *backdraft.Agent {
		return serve.Agent(ctx, name, cfg, towerAddr)
	}
	start := func(name string, a *backdraft.Agent, register func(*grpc.Server)) (string, error) {
		lis, err := listen("")
		if err != nil {
			return "", err
		}
		gs := grpc.NewServer(a.ServerOptions()...)
		register(gs)
		go serve.GRPC(ctx, name, lis, gs)
		return lis.Addr().String(), nil
	}

	accountAddr, err := start(reference.Account, agent(reference.Account, reference.AccountPolicy()), func(gs *grpc.Server) {
		account.Register(gs, account.NewService(account.NewMemoryRepository()))
	})
	if err != nil {
		return nil, err
	}
	catalogAddr, err := start(reference.Catalog, agent(reference.Catalog, reference.CatalogPolicy()), func(gs *grpc.Server) {
		catalog.Register(gs, catalog.NewService(catalog.NewMemoryRepository()))
	})
	if err != nil {
		return nil, err
	}

	orderAgent := agent(reference.Order, reference.OrderPolicy())
	orderAccounts, err := account.NewClient(accountAddr, orderAgent.DialOptions(reference.Account)...)
	if err != nil {
		return nil, err
	}
	orderProducts, err := catalog.NewClient(catalogAddr, orderAgent.DialOptions(reference.Catalog)...)
	if err != nil {
		return nil, err
	}
	orderAddr, err := start(reference.Order, orderAgent, func(gs *grpc.Server) {
		order.Register(gs, order.NewService(order.NewMemoryRepository(), orderAccounts, orderProducts))
	})
	if err != nil {
		return nil, err
	}

	gatewayAgent := agent(reference.Gateway, reference.GatewayPolicy())
	gwAccounts, err := account.NewClient(accountAddr, gatewayAgent.DialOptions(reference.Account)...)
	if err != nil {
		return nil, err
	}
	gwProducts, err := catalog.NewClient(catalogAddr, gatewayAgent.DialOptions(reference.Catalog)...)
	if err != nil {
		return nil, err
	}
	gwOrders, err := order.NewClient(orderAddr, gatewayAgent.DialOptions(reference.Order)...)
	if err != nil {
		return nil, err
	}
	gateway := &http.Server{Handler: graphql.Routes(graphql.NewServer(gwAccounts, gwProducts, gwOrders), gatewayAgent)}
	go gateway.Serve(gatewayLis)

	api := &http.Server{Handler: withDevCORS(t.Handler())}
	go api.Serve(httpLis)

	go func() {
		<-ctx.Done()
		gateway.Close()
		api.Close()
		for _, c := range []interface{ Close() }{orderAccounts, orderProducts, gwAccounts, gwProducts, gwOrders} {
			c.Close()
		}
	}()
	go t.Run(ctx, towerAddr)

	return &Lab{
		Tower:   t,
		HTTP:    api,
		API:     "http://" + httpLis.Addr().String(),
		Gateway: fmt.Sprintf("http://%s/graphql", gatewayLis.Addr()),
	}, nil
}

// withDevCORS lets the Vite dev server on its default port call the API.
func withDevCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o == "http://localhost:5173" || o == "http://127.0.0.1:5173" {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
