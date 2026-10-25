package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/SanishKumar/go-microservices/internal/lab"
	"github.com/SanishKumar/go-microservices/internal/serve"
	webassets "github.com/SanishKumar/go-microservices/web"
)

func runDemo(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	http := flags.String("http", "127.0.0.1:7070", "workspace address")
	gateway := flags.String("gateway", "127.0.0.1:8080", "reference GraphQL gateway")
	data := flags.String("data", ".backdraft/reviews", "saved review directory (empty uses memory)")
	ui := flags.String("ui", "", "optional UI directory override")
	rps := flags.Float64("rps", 150, "reference traffic rate")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: backdraft demo [flags]")
		return 2
	}
	ctx, stop := serve.Context()
	defer stop()
	running, err := lab.Start(ctx, lab.Options{HTTPAddr: *http, GatewayAddr: *gateway, DataDir: *data, UIDir: *ui, UIFS: webassets.Files(), RPS: *rps})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "Review workspace:", running.API)
	<-ctx.Done()
	return 0
}
