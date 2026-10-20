package order

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/SanishKumar/go-microservices/account"
	"github.com/SanishKumar/go-microservices/catalog"
	"github.com/SanishKumar/go-microservices/internal/money"
	"github.com/segmentio/ksuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrInvalidOrder    = errors.New("invalid order")
	ErrAccountNotFound = errors.New("account not found")
	ErrNotFound        = errors.New("order not found")
)

// recentOrdersLimit bounds GetOrdersForAccount so an account page does not
// grow without limit as an account accumulates orders.
const recentOrdersLimit = 25

type Service interface {
	// PostOrder places an order. When idempotencyKey was already used by the
	// account, the original order is returned with replayed=true and nothing
	// new is written.
	PostOrder(ctx context.Context, accountID string, lines []Line, idempotencyKey string) (o *Order, replayed bool, err error)
	GetOrdersForAccount(ctx context.Context, accountID string) ([]Order, error)
}

// Line is one requested product and quantity.
type Line struct {
	ProductID string
	Quantity  uint32
}

type Order struct {
	ID             string
	CreatedAt      time.Time
	TotalPrice     float64
	AccountID      string
	IdempotencyKey string
	Products       []OrderedProduct
}

// OrderedProduct is a line item. Name, Description and Price are snapshots
// taken when the order was placed so history does not change when the
// catalog does.
type OrderedProduct struct {
	ID          string
	Name        string
	Description string
	Price       float64
	Quantity    uint32
}

// AccountLookup and ProductLookup are the slices of the account and catalog
// clients the order service depends on.
type AccountLookup interface {
	GetAccount(ctx context.Context, id string) (*account.Account, error)
}

type ProductLookup interface {
	GetProducts(ctx context.Context, skip, take uint64, ids []string, query string) ([]catalog.Product, error)
}

type orderService struct {
	repository Repository
	accounts   AccountLookup
	products   ProductLookup
}

func NewService(r Repository, accounts AccountLookup, products ProductLookup) Service {
	return &orderService{repository: r, accounts: accounts, products: products}
}

func (s *orderService) PostOrder(ctx context.Context, accountID string, lines []Line, idempotencyKey string) (*Order, bool, error) {
	if accountID == "" {
		return nil, false, fmt.Errorf("%w: account id is required", ErrInvalidOrder)
	}
	merged, err := mergeLines(lines)
	if err != nil {
		return nil, false, err
	}

	// A replayed key is answered from our own store before touching any
	// dependency, so retries of a slow-but-successful call stay cheap.
	if idempotencyKey != "" {
		existing, err := s.repository.GetOrderByIdempotencyKey(ctx, accountID, idempotencyKey)
		if err == nil {
			return existing, true, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
	}

	if _, err := s.accounts.GetAccount(ctx, accountID); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, false, ErrAccountNotFound
		}
		// Anything else (unavailable, deadline exceeded) is passed through
		// with its status so callers can tell transient from permanent.
		return nil, false, err
	}

	ids := make([]string, len(merged))
	for i, l := range merged {
		ids[i] = l.ProductID
	}
	found, err := s.products.GetProducts(ctx, 0, 0, ids, "")
	if err != nil {
		return nil, false, err
	}
	byID := make(map[string]catalog.Product, len(found))
	for _, p := range found {
		byID[p.ID] = p
	}

	o := &Order{
		ID:             ksuid.New().String(),
		CreatedAt:      time.Now().UTC(),
		AccountID:      accountID,
		IdempotencyKey: idempotencyKey,
	}
	var missing []string
	var totalCents int64
	for _, l := range merged {
		p, ok := byID[l.ProductID]
		if !ok {
			missing = append(missing, l.ProductID)
			continue
		}
		cents, valid := money.Cents(p.Price)
		if !valid || cents > (money.MaxCents-totalCents)/int64(l.Quantity) {
			return nil, false, fmt.Errorf("%w: order total exceeds the supported price range", ErrInvalidOrder)
		}
		totalCents += cents * int64(l.Quantity)
		o.Products = append(o.Products, OrderedProduct{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Price:       float64(cents) / 100,
			Quantity:    l.Quantity,
		})
	}
	if len(missing) > 0 {
		return nil, false, fmt.Errorf("%w: unknown products %s", ErrInvalidOrder, strings.Join(missing, ", "))
	}
	o.TotalPrice = float64(totalCents) / 100

	stored, created, err := s.repository.PutOrder(ctx, *o)
	if err != nil {
		return nil, false, err
	}
	return stored, !created, nil
}

func (s *orderService) GetOrdersForAccount(ctx context.Context, accountID string) ([]Order, error) {
	return s.repository.GetOrdersForAccount(ctx, accountID, recentOrdersLimit)
}

// mergeLines validates lines and folds repeated products into one line.
// Repeats used to violate the (order_id, product_id) primary key and fail
// the whole order with an opaque error.
func mergeLines(lines []Line) ([]Line, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: an order needs at least one product", ErrInvalidOrder)
	}
	qty := map[string]uint32{}
	for _, l := range lines {
		if l.ProductID == "" || l.Quantity == 0 {
			return nil, fmt.Errorf("%w: every line needs a product id and a positive quantity", ErrInvalidOrder)
		}
		if uint64(qty[l.ProductID])+uint64(l.Quantity) > math.MaxInt32 {
			return nil, fmt.Errorf("%w: product quantity exceeds %d", ErrInvalidOrder, int64(math.MaxInt32))
		}
		qty[l.ProductID] += l.Quantity
	}
	merged := make([]Line, 0, len(qty))
	for id, q := range qty {
		merged = append(merged, Line{ProductID: id, Quantity: q})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ProductID < merged[j].ProductID })
	return merged, nil
}
