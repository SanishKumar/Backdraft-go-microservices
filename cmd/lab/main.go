// Command lab runs the whole reference system and the Backdraft control
// plane in one process, with in-memory stores: no Docker, no databases.
//
//	go run ./cmd/lab            then open http://localhost:7070
package main

import (
	"flag"
	"log"

	"github.com/SanishKumar/go-microservices/internal/lab"
	"github.com/SanishKumar/go-microservices/internal/serve"
)

func main() {
	httpAddr := flag.String("http", "127.0.0.1:7070", "control plane API and UI")
	gatewayAddr := flag.String("gateway", "127.0.0.1:8080", "GraphQL gateway")
	ui := flag.String("ui", "web/dist", "built web UI to serve (skipped if missing)")
	data := flag.String("data", ".backdraft/reviews", "saved review directory (empty keeps reviews in memory)")
	rps := flag.Float64("rps", 150, "initial load, requests per second")
	flag.Parse()

	ctx, stop := serve.Context()
	defer stop()
	l, err := lab.Start(ctx, lab.Options{HTTPAddr: *httpAddr, GatewayAddr: *gatewayAddr, UIDir: *ui, DataDir: *data, RPS: *rps})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("lab: open %s  (GraphQL playground: %s)", l.API, l.Gateway[:len(l.Gateway)-len("/graphql")]+"/playground")
	<-ctx.Done()
}
