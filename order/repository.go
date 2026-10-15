package order

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/lib/pq"
)

type Repository interface {
	Close()
	// PutOrder stores o. If o carries an idempotency key the account already
	// used, the existing order is returned with created=false.
	PutOrder(ctx context.Context, o Order) (stored *Order, created bool, err error)
	GetOrderByIdempotencyKey(ctx context.Context, accountID, key string) (*Order, error)
	// GetOrdersForAccount returns the newest orders first.
	GetOrdersForAccount(ctx context.Context, accountID string, limit int) ([]Order, error)
}

type postgresRepository struct {
	db *sql.DB
}

func NewPostgresRepository(url string) (Repository, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &postgresRepository{db}, nil
}

func (r *postgresRepository) Close() {
	r.db.Close()
}

func (r *postgresRepository) PutOrder(ctx context.Context, o Order) (stored *Order, created bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if err != nil || !created {
			tx.Rollback()
		}
	}()

	var id string
	err = tx.QueryRowContext(
		ctx,
		`INSERT INTO orders(id, created_at, account_id, total_price, idempotency_key)
		 VALUES($1, $2, $3, $4, NULLIF($5, ''))
		 ON CONFLICT (account_id, idempotency_key) DO NOTHING
		 RETURNING id`,
		o.ID, o.CreatedAt, o.AccountID, o.TotalPrice, o.IdempotencyKey,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Lost the race to a concurrent request with the same key; its
		// insert has committed by the time ON CONFLICT resolved.
		existing, err := r.GetOrderByIdempotencyKey(ctx, o.AccountID, o.IdempotencyKey)
		return existing, false, err
	}
	if err != nil {
		return nil, false, err
	}

	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("order_products", "order_id", "product_id", "name", "description", "price", "quantity"))
	if err != nil {
		return nil, false, err
	}
	for _, p := range o.Products {
		if _, err = stmt.ExecContext(ctx, o.ID, p.ID, p.Name, p.Description, p.Price, p.Quantity); err != nil {
			stmt.Close()
			return nil, false, err
		}
	}
	// An argument-less Exec flushes the COPY buffer; the statement must be
	// closed before the transaction commits.
	if _, err = stmt.ExecContext(ctx); err != nil {
		stmt.Close()
		return nil, false, err
	}
	if err = stmt.Close(); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	created = true
	return &o, true, nil
}

const orderColumns = `o.id, o.created_at, o.account_id, o.total_price, COALESCE(o.idempotency_key, ''),
	op.product_id, op.name, op.description, op.price, op.quantity`

func (r *postgresRepository) GetOrderByIdempotencyKey(ctx context.Context, accountID, key string) (*Order, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+orderColumns+`
		 FROM orders o JOIN order_products op ON op.order_id = o.id
		 WHERE o.account_id = $1 AND o.idempotency_key = $2
		 ORDER BY op.product_id`,
		accountID, key,
	)
	if err != nil {
		return nil, err
	}
	orders, err := scanOrders(rows)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, ErrNotFound
	}
	return &orders[0], nil
}

func (r *postgresRepository) GetOrdersForAccount(ctx context.Context, accountID string, limit int) ([]Order, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+orderColumns+`
		 FROM (SELECT * FROM orders WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2) o
		 JOIN order_products op ON op.order_id = o.id
		 ORDER BY o.created_at DESC, o.id DESC, op.product_id`,
		accountID, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanOrders(rows)
}

// scanOrders folds joined order/line rows, which arrive grouped by order,
// into orders.
func scanOrders(rows *sql.Rows) ([]Order, error) {
	defer rows.Close()
	orders := []Order{}
	for rows.Next() {
		var o Order
		var p OrderedProduct
		if err := rows.Scan(
			&o.ID, &o.CreatedAt, &o.AccountID, &o.TotalPrice, &o.IdempotencyKey,
			&p.ID, &p.Name, &p.Description, &p.Price, &p.Quantity,
		); err != nil {
			return nil, err
		}
		if n := len(orders); n == 0 || orders[n-1].ID != o.ID {
			orders = append(orders, o)
		}
		last := &orders[len(orders)-1]
		last.Products = append(last.Products, p)
	}
	return orders, rows.Err()
}

// memoryRepository is the in-process order store used by the lab and tests.
type memoryRepository struct {
	mu        sync.RWMutex
	byID      map[string]Order
	byAccount map[string][]string // oldest first
	byKey     map[string]string   // accountID + "\x00" + key -> order id
}

func NewMemoryRepository() Repository {
	return &memoryRepository{
		byID:      map[string]Order{},
		byAccount: map[string][]string{},
		byKey:     map[string]string{},
	}
}

func (r *memoryRepository) Close() {}

func (r *memoryRepository) PutOrder(_ context.Context, o Order) (*Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.IdempotencyKey != "" {
		k := o.AccountID + "\x00" + o.IdempotencyKey
		if id, ok := r.byKey[k]; ok {
			existing := r.byID[id]
			return &existing, false, nil
		}
		r.byKey[k] = o.ID
	}
	r.byID[o.ID] = o
	ids := append(r.byAccount[o.AccountID], o.ID)
	// Keep memory bounded for long-running labs; reads only see the newest.
	if len(ids) > 4*recentOrdersLimit {
		for _, old := range ids[:len(ids)-recentOrdersLimit] {
			if k := r.byID[old].IdempotencyKey; k != "" {
				delete(r.byKey, o.AccountID+"\x00"+k)
			}
			delete(r.byID, old)
		}
		ids = append([]string(nil), ids[len(ids)-recentOrdersLimit:]...)
	}
	r.byAccount[o.AccountID] = ids
	return &o, true, nil
}

func (r *memoryRepository) GetOrderByIdempotencyKey(_ context.Context, accountID, key string) (*Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byKey[accountID+"\x00"+key]
	if !ok {
		return nil, ErrNotFound
	}
	o := r.byID[id]
	return &o, nil
}

func (r *memoryRepository) GetOrdersForAccount(_ context.Context, accountID string, limit int) ([]Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.byAccount[accountID]
	orders := []Order{}
	for i := len(ids) - 1; i >= 0 && len(orders) < limit; i-- {
		orders = append(orders, r.byID[ids[i]])
	}
	return orders, nil
}
