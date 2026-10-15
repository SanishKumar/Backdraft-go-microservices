package main

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/internal/reference"
	"github.com/SanishKumar/go-microservices/internal/serve"
	"github.com/SanishKumar/go-microservices/order"
	"github.com/kelseyhightower/envconfig"
	"github.com/tinrab/retry"
	"google.golang.org/grpc"
)

type Config struct {
	// DatabaseURL is a Postgres URL, or "memory" for an in-process store.
	DatabaseURL  string `envconfig:"DATABASE_URL" default:"memory"`
	AccountURL   string `envconfig:"ACCOUNT_SERVICE_URL" required:"true"`
	CatalogURL   string `envconfig:"CATALOG_SERVICE_URL" required:"true"`
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

	var r order.Repository
	if cfg.DatabaseURL == "memory" {
		r = order.NewMemoryRepository()
	} else {
		retry.ForeverSleep(2*time.Second, func(_ int) (err error) {
			if r, err = order.NewPostgresRepository(cfg.DatabaseURL); err != nil {
				log.Println(err)
			}
			return
		})
	}
	defer r.Close()

	agent := serve.Agent(ctx, reference.Order, reference.OrderPolicy(), cfg.BackdraftURL)
	accounts, err := account.NewClient(cfg.AccountURL, agent.DialOptions(reference.Account)...)
	if err != nil {
		log.Fatal(err)
	}
	defer accounts.Close()
	products, err := catalog.NewClient(cfg.CatalogURL, agent.DialOptions(reference.Catalog)...)
	if err != nil {
		log.Fatal(err)
	}
	defer products.Close()

	gs := grpc.NewServer(agent.ServerOptions()...)
	order.Register(gs, order.NewService(r, accounts, products))
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		log.Fatal(err)
	}
	if err := serve.GRPC(ctx, reference.Order, lis, gs); err != nil {
		log.Fatal(err)
	}
}
