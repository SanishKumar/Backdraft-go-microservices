package main

import (
	"fmt"
	"log"

	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/graphql"
	"github.com/SanishKumar/go-microservices/internal/reference"
	"github.com/SanishKumar/go-microservices/internal/serve"
	"github.com/SanishKumar/go-microservices/order"
	"github.com/kelseyhightower/envconfig"
)

type AppConfig struct {
	AccountURL   string `envconfig:"ACCOUNT_SERVICE_URL" required:"true"`
	CatalogURL   string `envconfig:"CATALOG_SERVICE_URL" required:"true"`
	OrderURL     string `envconfig:"ORDER_SERVICE_URL" required:"true"`
	BackdraftURL string `envconfig:"BACKDRAFT_URL"`
	Port         int    `envconfig:"PORT" default:"8080"`
}

func main() {
	var cfg AppConfig
	if err := envconfig.Process("", &cfg); err != nil {
		log.Fatal(err)
	}
	ctx, stop := serve.Context()
	defer stop()

	agent := serve.Agent(ctx, reference.Gateway, reference.GatewayPolicy(), cfg.BackdraftURL)
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
	orders, err := order.NewClient(cfg.OrderURL, agent.DialOptions(reference.Order)...)
	if err != nil {
		log.Fatal(err)
	}
	defer orders.Close()

	h := graphql.Routes(graphql.NewServer(accounts, products, orders), agent)
	if err := serve.HTTP(ctx, reference.Gateway, fmt.Sprintf(":%d", cfg.Port), h); err != nil {
		log.Fatal(err)
	}
}
