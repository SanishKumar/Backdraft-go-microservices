package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	elastic "gopkg.in/olivere/elastic.v5"
)

func TestFreshElasticCatalogCanBeReadBeforeFirstWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"type":"index_not_found_exception","reason":"no such index"},"status":404}`))
	}))
	defer srv.Close()
	c, err := elastic.NewClient(elastic.SetURL(srv.URL), elastic.SetSniff(false), elastic.SetHealthcheck(false))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	r := &elasticRepository{client: c}
	for _, search := range []bool{false, true} {
		var got []Product
		if search {
			got, err = r.SearchProducts(context.Background(), "lamp", 0, 10)
		} else {
			got, err = r.ListProducts(context.Background(), 0, 10)
		}
		if err != nil || len(got) != 0 {
			t.Fatalf("search=%v: got %v, %v; want an empty catalog", search, got, err)
		}
	}
}
