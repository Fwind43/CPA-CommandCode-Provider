package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDynamicCatalog(t *testing.T) {
	hits := 0
	body := `{"data":[{"id":"xiaomi/mimo-v2.6-pro","name":"MiMo V2.6 Pro","context_length":1048576,"supported_endpoints":["/chat/completions"]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked")
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c := &modelCatalog{client: srv.Client(), url: srv.URL}
	original := commandCodeCatalog
	commandCodeCatalog = c
	defer func() { commandCodeCatalog = original }()
	models := commandCodeModels()
	if len(models) != 1 || models[0].ID != "mimo-v2.6-pro" || models[0].ContextLength != 1048576 {
		t.Fatalf("models: %+v", models)
	}
	if upstreamModelID("mimo-v2.6-pro") != "xiaomi/mimo-v2.6-pro" {
		t.Fatal("alias")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.snapshot() }()
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("cache hits %d", hits)
	}
	for _, bad := range []string{`{"data":[]}`, `no JSON`, `{"data":[{"id":"bad id"}]}`} {
		body = bad
		c.nextRefresh = time.Time{}
		if got := c.snapshot(); len(got) != 1 || got[0].ID != "xiaomi/mimo-v2.6-pro" {
			t.Fatal("lost last good")
		}
	}
	body = `{"data":[{"id":"a/shared"},{"id":"b/shared"},{"id":"embed","supported_endpoints":["/embeddings"]},{"id":"a/shared"}]}`
	c.nextRefresh = time.Time{}
	got := c.snapshot()
	if len(got) != 2 || catalogPublicID("a/shared", got) != "a/shared" || upstreamModelID("shared") != "shared" {
		t.Fatal("collision/filter")
	}
	body = `bad`
	c.models = nil
	c.nextRefresh = time.Time{}
	if len(c.snapshot()) != len(goPlanModelIDs) {
		t.Fatal("cold fallback")
	}
}

func TestLiveCatalog(t *testing.T) {
	if os.Getenv("COMMANDCODE_TEST_LIVE_MODELS") != "1" {
		t.Skip("explicit live opt-in")
	}
	models, err := commandCodeCatalog.fetch()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.ID == "xiaomi/mimo-v2.6-pro" {
			t.Logf("live models=%d MiMo context=%d", len(models), m.ContextLength)
			return
		}
	}
	t.Fatal("MiMo missing from official catalog")
}
