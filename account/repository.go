package account

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"

	_ "github.com/lib/pq"
)

type Repository interface {
	Close()
	PutAccount(ctx context.Context, a Account) error
	GetAccountByID(ctx context.Context, id string) (*Account, error)
	ListAccounts(ctx context.Context, skip uint64, take uint64) ([]Account, error)
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

func (r *postgresRepository) PutAccount(ctx context.Context, a Account) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO accounts(id, name) VALUES($1, $2)", a.ID, a.Name)
	return err
}

func (r *postgresRepository) GetAccountByID(ctx context.Context, id string) (*Account, error) {
	row := r.db.QueryRowContext(ctx, "SELECT id, name FROM accounts WHERE id = $1", id)
	a := &Account{}
	if err := row.Scan(&a.ID, &a.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return a, nil
}

func (r *postgresRepository) ListAccounts(ctx context.Context, skip uint64, take uint64) ([]Account, error) {
	rows, err := r.db.QueryContext(
		ctx,
		"SELECT id, name FROM accounts ORDER BY id DESC OFFSET $1 LIMIT $2",
		skip,
		take,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []Account{}
	for rows.Next() {
		a := Account{}
		if err = rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

// memoryRepository keeps accounts in process memory. It backs the
// zero-dependency lab and tests; ordering matches the Postgres repository.
type memoryRepository struct {
	mu       sync.RWMutex
	byID     map[string]Account
	sortedID []string // descending
}

func NewMemoryRepository() Repository {
	return &memoryRepository{byID: map[string]Account{}}
}

func (r *memoryRepository) Close() {}

func (r *memoryRepository) PutAccount(_ context.Context, a Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[a.ID]; ok {
		return errors.New("duplicate account id")
	}
	r.byID[a.ID] = a
	i := sort.Search(len(r.sortedID), func(i int) bool { return r.sortedID[i] < a.ID })
	r.sortedID = append(r.sortedID, "")
	copy(r.sortedID[i+1:], r.sortedID[i:])
	r.sortedID[i] = a.ID
	return nil
}

func (r *memoryRepository) GetAccountByID(_ context.Context, id string) (*Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &a, nil
}

func (r *memoryRepository) ListAccounts(_ context.Context, skip uint64, take uint64) ([]Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	accounts := []Account{}
	for i := skip; i < uint64(len(r.sortedID)) && uint64(len(accounts)) < take; i++ {
		accounts = append(accounts, r.byID[r.sortedID[i]])
	}
	return accounts, nil
}
