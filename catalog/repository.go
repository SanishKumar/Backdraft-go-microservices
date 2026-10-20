package catalog

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	elastic "gopkg.in/olivere/elastic.v5"
)

type Repository interface {
	Close()
	PutProduct(ctx context.Context, p Product) error
	GetProductByID(ctx context.Context, id string) (*Product, error)
	ListProducts(ctx context.Context, skip uint64, take uint64) ([]Product, error)
	ListProductsWithIDs(ctx context.Context, ids []string) ([]Product, error)
	SearchProducts(ctx context.Context, query string, skip uint64, take uint64) ([]Product, error)
}

type elasticRepository struct {
	client *elastic.Client
}

type productDocument struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
}

func NewElasticRepository(url string) (Repository, error) {
	client, err := elastic.NewClient(
		elastic.SetURL(url),
		elastic.SetSniff(false),
	)
	if err != nil {
		return nil, err
	}
	return &elasticRepository{client}, nil
}

func (r *elasticRepository) Close() {
	r.client.Stop()
}

func (r *elasticRepository) PutProduct(ctx context.Context, p Product) error {
	_, err := r.client.Index().
		Index("catalog").
		Type("product").
		Id(p.ID).
		BodyJson(productDocument{
			Name:        p.Name,
			Description: p.Description,
			Price:       p.Price,
		}).
		Do(ctx)
	return err
}

func (r *elasticRepository) GetProductByID(ctx context.Context, id string) (*Product, error) {
	res, err := r.client.Get().
		Index("catalog").
		Type("product").
		Id(id).
		Do(ctx)
	if elastic.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !res.Found || res.Source == nil {
		return nil, ErrNotFound
	}
	p := productDocument{}
	if err = json.Unmarshal(*res.Source, &p); err != nil {
		return nil, err
	}
	return &Product{ID: id, Name: p.Name, Description: p.Description, Price: p.Price}, nil
}

func (r *elasticRepository) ListProducts(ctx context.Context, skip, take uint64) ([]Product, error) {
	res, err := r.client.Search().
		Index("catalog").
		Type("product").
		Query(elastic.NewMatchAllQuery()).
		From(int(skip)).Size(int(take)).
		Do(ctx)
	// A fresh Elasticsearch database has no catalog index until the first
	// write. Treat it as an empty catalog so the load generator can seed it.
	if elastic.IsNotFound(err) {
		return []Product{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeHits(res.Hits.Hits)
}

func (r *elasticRepository) ListProductsWithIDs(ctx context.Context, ids []string) ([]Product, error) {
	if len(ids) == 0 {
		return []Product{}, nil
	}
	items := make([]*elastic.MultiGetItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, elastic.NewMultiGetItem().Index("catalog").Type("product").Id(id))
	}
	res, err := r.client.MultiGet().Add(items...).Do(ctx)
	if err != nil {
		return nil, err
	}
	products := []Product{}
	for _, doc := range res.Docs {
		// Missing documents come back with Found=false and a nil Source;
		// dereferencing it used to panic and take the whole service down.
		if !doc.Found || doc.Source == nil {
			continue
		}
		p := productDocument{}
		if err := json.Unmarshal(*doc.Source, &p); err != nil {
			return nil, err
		}
		products = append(products, Product{ID: doc.Id, Name: p.Name, Description: p.Description, Price: p.Price})
	}
	return products, nil
}

func (r *elasticRepository) SearchProducts(ctx context.Context, query string, skip, take uint64) ([]Product, error) {
	res, err := r.client.Search().
		Index("catalog").
		Type("product").
		Query(elastic.NewMultiMatchQuery(query, "name", "description")).
		From(int(skip)).Size(int(take)).
		Do(ctx)
	if elastic.IsNotFound(err) {
		return []Product{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeHits(res.Hits.Hits)
}

func decodeHits(hits []*elastic.SearchHit) ([]Product, error) {
	products := make([]Product, 0, len(hits))
	for _, hit := range hits {
		if hit.Source == nil {
			continue
		}
		p := productDocument{}
		if err := json.Unmarshal(*hit.Source, &p); err != nil {
			return nil, err
		}
		products = append(products, Product{ID: hit.Id, Name: p.Name, Description: p.Description, Price: p.Price})
	}
	return products, nil
}

// memoryRepository is an in-process catalog with a small term-match search,
// used by the lab and tests in place of Elasticsearch.
type memoryRepository struct {
	mu   sync.RWMutex
	byID map[string]Product
	ids  []string // ascending
}

func NewMemoryRepository() Repository {
	return &memoryRepository{byID: map[string]Product{}}
}

func (r *memoryRepository) Close() {}

func (r *memoryRepository) PutProduct(_ context.Context, p Product) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[p.ID]; !ok {
		i := sort.SearchStrings(r.ids, p.ID)
		r.ids = append(r.ids, "")
		copy(r.ids[i+1:], r.ids[i:])
		r.ids[i] = p.ID
	}
	r.byID[p.ID] = p
	return nil
}

func (r *memoryRepository) GetProductByID(_ context.Context, id string) (*Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &p, nil
}

func (r *memoryRepository) ListProducts(_ context.Context, skip, take uint64) ([]Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	products := []Product{}
	for i := skip; i < uint64(len(r.ids)) && uint64(len(products)) < take; i++ {
		products = append(products, r.byID[r.ids[i]])
	}
	return products, nil
}

func (r *memoryRepository) ListProductsWithIDs(_ context.Context, ids []string) ([]Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	products := []Product{}
	for _, id := range ids {
		if p, ok := r.byID[id]; ok {
			products = append(products, p)
		}
	}
	return products, nil
}

func (r *memoryRepository) SearchProducts(_ context.Context, query string, skip, take uint64) ([]Product, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return []Product{}, nil
	}
	type scored struct {
		p     Product
		score int
	}
	r.mu.RLock()
	var hits []scored
	for _, id := range r.ids {
		p := r.byID[id]
		text := strings.ToLower(p.Name + " " + p.Description)
		score := 0
		for _, t := range terms {
			score += strings.Count(text, t)
		}
		if score > 0 {
			hits = append(hits, scored{p, score})
		}
	}
	r.mu.RUnlock()
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	products := []Product{}
	for i := skip; i < uint64(len(hits)) && uint64(len(products)) < take; i++ {
		products = append(products, hits[i].p)
	}
	return products, nil
}
