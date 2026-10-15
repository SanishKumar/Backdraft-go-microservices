// Package serve holds the process plumbing shared by every binary:
// signal handling, graceful shutdown, and connecting to Backdraft.
package serve

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Context is cancelled on SIGINT or SIGTERM.
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Agent creates the service's Backdraft agent and, if controlPlane is
// set, keeps it connected for the life of ctx.
func Agent(ctx context.Context, service string, declared backdraft.ServiceConfig, controlPlane string) *backdraft.Agent {
	a := backdraft.New(service, declared)
	if controlPlane != "" {
		go a.Run(ctx, controlPlane)
	} else {
		log.Printf("%s: BACKDRAFT_URL not set; enforcing compiled-in policy without a control plane", service)
	}
	return a
}

// GRPC serves gs on lis until ctx ends, then drains in-flight requests.
func GRPC(ctx context.Context, name string, lis net.Listener, gs *grpc.Server) error {
	reflection.Register(gs)
	errc := make(chan error, 1)
	go func() { errc <- gs.Serve(lis) }()
	log.Printf("%s: gRPC listening on %s", name, lis.Addr())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	stopped := make(chan struct{})
	go func() { gs.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		gs.Stop()
	}
	return nil
}

// HTTP serves h on addr until ctx ends, then shuts down gracefully.
func HTTP(ctx context.Context, name, addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("%s: HTTP listening on %s", name, addr)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
