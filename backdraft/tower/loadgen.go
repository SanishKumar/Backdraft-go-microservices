package tower

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SanishKumar/go-microservices/backdraft"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LoadConfig is what the load generator sends.
type LoadConfig struct {
	RPS float64 `json:"rps"`
	// Mix weights the operations: Browse, AccountPage, Checkout.
	Mix map[string]float64 `json:"mix"`
}

var defaultMix = map[string]float64{"Browse": 0.5, "AccountPage": 0.3, "Checkout": 0.2}

func (c LoadConfig) Validate() error {
	if math.IsNaN(c.RPS) || math.IsInf(c.RPS, 0) || c.RPS < 0 || c.RPS > 5000 {
		return fmt.Errorf("rps must be within 0-5000")
	}
	if c.Mix != nil {
		total := 0.0
		for op, weight := range c.Mix {
			if _, ok := defaultMix[op]; !ok {
				return fmt.Errorf("unknown load operation %q", op)
			}
			if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				return fmt.Errorf("mix weights must be finite and non-negative")
			}
			total += weight
		}
		if total <= 0 || math.IsInf(total, 0) {
			return fmt.Errorf("mix must have positive finite total weight")
		}
	}
	return nil
}

// loadGen is an open-loop load generator: requests arrive as a Poisson
// process at the configured rate whether or not earlier ones have
// finished, as real users do. A closed-loop generator (N workers in a
// loop) slows down when the system does and hides exactly the overload
// this tool is about.
type loadGen struct {
	target  string
	agent   *backdraft.Agent
	client  *http.Client
	cfg     atomic.Pointer[LoadConfig]
	metrics *metrics

	mu       sync.RWMutex
	accounts []string
	products []string
	ready    atomic.Bool
}

func newLoadGen(target string, agent *backdraft.Agent, m *metrics) *loadGen {
	l := &loadGen{
		target:  target,
		agent:   agent,
		metrics: m,
		client: &http.Client{Transport: &http.Transport{
			MaxIdleConns:        2048,
			MaxIdleConnsPerHost: 2048,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  true,
		}},
	}
	l.cfg.Store(&LoadConfig{Mix: defaultMix})
	return l
}

func (l *loadGen) config() LoadConfig { return *l.cfg.Load() }

func (l *loadGen) set(c LoadConfig) {
	if c.Mix == nil {
		c.Mix = l.config().Mix
	}
	c.RPS = math.Max(0, math.Min(c.RPS, 5000))
	l.cfg.Store(&c)
}

func (l *loadGen) run(ctx context.Context) {
	for ctx.Err() == nil && !l.ready.Load() {
		if err := l.seed(ctx); err != nil {
			log.Printf("backdraft: load generator waiting for %s: %v", l.target, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		l.ready.Store(true)
	}
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 11))
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cfg := l.config()
			elapsed := now.Sub(last).Seconds()
			last = now
			for n := poisson(rng, cfg.RPS*elapsed); n > 0; n-- {
				go l.request(ctx, pick(rng, cfg.Mix), rng.Uint64())
			}
		}
	}
}

func poisson(rng *rand.Rand, mean float64) int {
	if mean <= 0 {
		return 0
	}
	if mean > 30 {
		return max(0, int(math.Round(mean+math.Sqrt(mean)*rng.NormFloat64())))
	}
	l, k, p := math.Exp(-mean), 0, 1.0
	for {
		p *= rng.Float64()
		if p <= l {
			return k
		}
		k++
	}
}

func pick(rng *rand.Rand, mix map[string]float64) string {
	total := 0.0
	for _, w := range mix {
		total += w
	}
	u := rng.Float64() * total
	for _, op := range sortedKeys(mix) {
		if u -= mix[op]; u <= 0 {
			return op
		}
	}
	return "Browse"
}

const (
	browseQuery  = `query Browse($q: String) { products(query: $q, pagination: {take: 12}) { id name price } }`
	accountQuery = `query AccountPage($id: String!) { accounts(id: $id) { id name orders { id totalPrice createdAt products { id name quantity price } } } }`
	checkoutMut  = `mutation Checkout($order: OrderInput!) { createOrder(order: $order) { id totalPrice } }`
)

var searchTerms = []string{"", "", "lamp", "desk", "chair", "mug", "notebook", "cable", "steel", "oak"}

func (l *loadGen) request(ctx context.Context, op string, seed uint64) {
	l.metrics.offered()
	rng := rand.New(rand.NewPCG(seed, 5))
	l.mu.RLock()
	account := l.accounts[rng.IntN(len(l.accounts))]
	product := l.products[rng.IntN(len(l.products))]
	other := l.products[rng.IntN(len(l.products))]
	l.mu.RUnlock()

	var query string
	var vars map[string]any
	headers := map[string]string{}
	switch op {
	case "AccountPage":
		query, vars = accountQuery, map[string]any{"id": account}
	case "Checkout":
		query = checkoutMut
		vars = map[string]any{"order": map[string]any{
			"accountId": account,
			"products": []map[string]any{
				{"id": product, "quantity": 1 + rng.IntN(3)},
				{"id": other, "quantity": 1},
			},
		}}
		// One key per purchase, reused if the user retries: a refresh
		// cannot buy twice.
		headers["Idempotency-Key"] = fmt.Sprintf("%016x", rng.Uint64())
	default:
		op = "Browse"
		query = browseQuery
		vars = map[string]any{}
		if q := searchTerms[rng.IntN(len(searchTerms))]; q != "" {
			vars["q"] = q
		}
	}

	start := time.Now()
	err := l.agent.Call(ctx, "gateway", op, func(ctx context.Context, at *backdraft.Attempt) error {
		h := map[string]string{"traceparent": at.Traceparent, "bd-caller": at.Caller, "bd-attempt": fmt.Sprint(at.N)}
		for k, v := range headers {
			h[k] = v
		}
		_, err := l.graphql(ctx, op, query, vars, h)
		return err
	})
	l.metrics.entry(err == nil, time.Since(start))
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

// graphql performs one request and maps failures to status codes the
// agent can classify.
func (l *loadGen) graphql(ctx context.Context, op, query string, vars map[string]any, headers map[string]string) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"operationName": op, "query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	switch {
	case resp.StatusCode == http.StatusGatewayTimeout:
		return nil, status.Error(codes.DeadlineExceeded, "gateway timeout")
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
		return nil, status.Error(codes.Unavailable, resp.Status)
	case resp.StatusCode >= 500:
		return nil, status.Error(codes.Internal, resp.Status)
	case resp.StatusCode >= 400:
		return nil, status.Error(codes.InvalidArgument, resp.Status)
	}
	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, status.Error(codes.Internal, "bad response: "+err.Error())
	}
	if len(gr.Errors) > 0 {
		code := codes.Internal
		if name, ok := gr.Errors[0].Extensions["code"].(string); ok {
			if c, ok := backdraft.ParseCode(name); ok {
				code = c
			}
		}
		return nil, status.Error(code, gr.Errors[0].Message)
	}
	return gr.Data, nil
}

// seed makes sure there are accounts and products to drive traffic at,
// creating them through the public API if the system is empty.
func (l *loadGen) seed(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids := func(query, field string) ([]string, error) {
		data, err := l.graphql(ctx, "", query, nil, nil)
		if err != nil {
			return nil, err
		}
		var out map[string][]struct{ ID string }
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		var ids []string
		for _, x := range out[field] {
			ids = append(ids, x.ID)
		}
		return ids, nil
	}
	accounts, err := ids(`{ accounts(pagination: {take: 100}) { id } }`, "accounts")
	if err != nil {
		return err
	}
	for i := len(accounts); i < len(seedAccounts); i++ {
		data, err := l.graphql(ctx, "", `mutation($n: String!) { createAccount(account: {name: $n}) { id } }`,
			map[string]any{"n": seedAccounts[i]}, nil)
		if err != nil {
			return err
		}
		var out struct{ CreateAccount struct{ ID string } }
		json.Unmarshal(data, &out)
		accounts = append(accounts, out.CreateAccount.ID)
	}
	products, err := ids(`{ products(pagination: {take: 100}) { id } }`, "products")
	if err != nil {
		return err
	}
	for i := len(products); i < len(seedProducts); i++ {
		p := seedProducts[i]
		data, err := l.graphql(ctx, "", `mutation($p: ProductInput!) { createProduct(product: $p) { id } }`,
			map[string]any{"p": map[string]any{"name": p.name, "description": p.desc, "price": p.price}}, nil)
		if err != nil {
			return err
		}
		var out struct{ CreateProduct struct{ ID string } }
		json.Unmarshal(data, &out)
		products = append(products, out.CreateProduct.ID)
	}
	l.mu.Lock()
	l.accounts, l.products = accounts, products
	l.mu.Unlock()
	return nil
}

var seedAccounts = []string{
	"Ada Okafor", "Bruno Silva", "Chen Wei", "Dana Kowalski", "Emeka Obi", "Farah Haddad",
	"Goran Petrov", "Hana Sato", "Ilse Novak", "Jon Varga", "Kemi Adeyemi", "Lars Berg",
	"Mira Iyer", "Nikolai Orlov", "Ola Nordmann", "Priya Raman", "Quinn Byrne", "Rosa Mendes",
	"Sami Laine", "Tomas Ruiz",
}

var seedProducts = []struct {
	name, desc string
	price      float64
}{
	{"Oak desk", "Solid oak writing desk with a steel frame", 349},
	{"Desk lamp", "Adjustable LED desk lamp, warm white", 59.5},
	{"Task chair", "Mesh task chair with lumbar support", 229},
	{"Ceramic mug", "Stoneware mug, 350 ml", 14},
	{"Dot-grid notebook", "A5 notebook, 160 pages", 18.75},
	{"USB-C cable", "Braided USB-C cable, 2 m", 12.99},
	{"Monitor arm", "Steel single monitor arm, gas spring", 119},
	{"Desk mat", "Felt desk mat, charcoal", 32},
	{"Fountain pen", "Steel nib fountain pen", 45},
	{"Cable tray", "Under-desk steel cable tray", 27.5},
	{"Floor lamp", "Arc floor lamp with linen shade", 189},
	{"Bookshelf", "Oak bookshelf, five shelves", 279},
	{"Laptop stand", "Aluminium laptop stand", 49},
	{"Headphone hook", "Under-desk headphone hook", 9.5},
	{"Plant pot", "Glazed ceramic plant pot", 22},
	{"Wall clock", "Silent oak wall clock", 39},
	{"Notebook set", "Three ruled notebooks", 21},
	{"Mechanical keyboard", "Tenkeyless keyboard, tactile switches", 139},
	{"Mouse", "Wireless ergonomic mouse", 69},
	{"Webcam", "1080p webcam with privacy shutter", 79},
	{"Coffee grinder", "Burr coffee grinder", 99},
	{"Steel bottle", "Insulated steel bottle, 750 ml", 29},
	{"Drawer unit", "Three-drawer steel pedestal", 199},
	{"Footrest", "Adjustable footrest", 44},
	{"Whiteboard", "Magnetic whiteboard, 90 x 60 cm", 65},
	{"Markers", "Whiteboard markers, eight colours", 11},
	{"Oak tray", "Oak valet tray", 34},
	{"Pencil set", "Graphite pencils, twelve grades", 16},
	{"Desk fan", "Quiet desk fan", 54},
	{"Power strip", "Six-outlet power strip with USB", 36},
}
