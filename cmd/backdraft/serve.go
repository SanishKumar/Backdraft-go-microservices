package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/SanishKumar/go-microservices/backdraft/pb"
	"github.com/SanishKumar/go-microservices/backdraft/tower"
	"github.com/SanishKumar/go-microservices/internal/reference"
	"github.com/SanishKumar/go-microservices/internal/serve"
	"google.golang.org/grpc"
)

const serveUsage = `usage: backdraft serve [flags]

Runs the control plane: agents connect to --grpc, the UI and API are on
--http. With --load-target, a load generator drives the reference
system's GraphQL API.

`

func runServe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	grpcAddr := fs.String("grpc", "127.0.0.1:7071", "address agents connect to")
	httpAddr := fs.String("http", "127.0.0.1:7070", "address of the API and UI")
	ui := fs.String("ui", "", "directory of the built web UI to serve")
	entry := fs.String("entry", "", "service receiving external traffic (default gateway, or users with --load-target)")
	target := fs.String("load-target", "", "GraphQL endpoint to drive with the load generator (empty disables it)")
	rps := fs.Float64("rps", 0, "initial load, requests per second")
	fs.Usage = func() {
		fmt.Fprint(stderr, serveUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if *entry == "" {
		*entry = reference.Gateway
		if *target != "" {
			*entry = reference.Users
		}
	}
	if *target != "" && *entry != reference.Users {
		fmt.Fprintln(stderr, "--load-target drives the reference system and requires --entry users")
		return 2
	}
	if err := (tower.LoadConfig{RPS: *rps}).Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	ctx, stop := serve.Context()
	defer stop()
	opts := tower.Options{
		Entry:       *entry,
		LoadTarget:  *target,
		EntryPolicy: reference.UsersPolicy(),
		InitialRPS:  *rps,
		UIDir:       *ui,
	}
	if *target != "" {
		opts.Fallback = reference.SimModel(reference.System())
	}
	t := tower.New(opts)

	lis, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Print(err)
		return 1
	}
	gs := grpc.NewServer()
	pb.RegisterControlPlaneServer(gs, t)
	go serve.GRPC(ctx, "backdraft", lis, gs)
	go t.Run(ctx, dialable(lis.Addr()))
	if err := serve.HTTP(ctx, "backdraft", *httpAddr, t.Handler()); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

// dialable turns a listener address like [::]:7071 into one a client in
// the same process can dial.
func dialable(a net.Addr) string {
	if tcp, ok := a.(*net.TCPAddr); ok && (tcp.IP == nil || tcp.IP.IsUnspecified()) {
		return fmt.Sprintf("127.0.0.1:%d", tcp.Port)
	}
	return a.String()
}
