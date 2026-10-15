package main

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/internal/reference"
	"github.com/SanishKumar/go-microservices/internal/serve"
	"github.com/kelseyhightower/envconfig"
	"github.com/tinrab/retry"
	"google.golang.org/grpc"
)

type Config struct {
	// DatabaseURL is an Elasticsearch URL, or "memory" for an in-process
	// store.
	DatabaseURL  string `envconfig:"DATABASE_URL" default:"memory"`
	BackdraftURL string `envconfig:"BACKDRAFT_URL"`
	Port         int    `envconfig:"PORT" default:"8080"`
}

func main() {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		log.Fatal(err)
	}
	ctx, stop := serve.Context()
	defer stop()

	var r catalog.Repository
	if cfg.DatabaseURL == "memory" {
		r = catalog.NewMemoryRepository()
	} else {
		retry.ForeverSleep(2*time.Second, func(_ int) (err error) {
			if r, err = catalog.NewElasticRepository(cfg.DatabaseURL); err != nil {
				log.Println(err)
			}
			return
		})
	}
	defer r.Close()

	agent := serve.Agent(ctx, reference.Catalog, reference.CatalogPolicy(), cfg.BackdraftURL)
	gs := grpc.NewServer(agent.ServerOptions()...)
	catalog.Register(gs, catalog.NewService(r))
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal(err)
	}
	if err := serve.GRPC(ctx, reference.Catalog, lis, gs); err != nil {
		log.Fatal(err)
	}
}
