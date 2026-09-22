package main

import (
	"testing"
	"time"
)

func TestPublicModelAliases(t *testing.T) {
	oldCatalog := commandCodeCatalog
	commandCodeCatalog = &modelCatalog{nextRefresh: time.Now().Add(time.Hour)}
	defer func() { commandCodeCatalog = oldCatalog }()
	seen := map[string]bool{}
	for i, model := range commandCodeModels() {
		if seen[model.ID] {
			t.Fatalf("duplicate %s", model.ID)
		}
		seen[model.ID] = true
		if upstreamModelID(model.ID) != goPlanModelIDs[i] {
			t.Fatalf("mapping %s", model.ID)
		}
		if upstreamModelID(goPlanModelIDs[i]) != goPlanModelIDs[i] {
			t.Fatal("canonical changed")
		}
	}
	if publicModelID("moonshotai/Kimi-K3") != "Kimi-K3" {
		t.Fatal("prefix retained")
	}
	if upstreamModelID("unknown") != "unknown" {
		t.Fatal("unknown changed")
	}
	original := goPlanModelIDs
	defer func() { goPlanModelIDs = original }()
	goPlanModelIDs = []string{"a/shared", "b/shared", "plain"}
	if publicModelID("a/shared") != "a/shared" || publicModelID("b/shared") != "b/shared" {
		t.Fatal("collision")
	}
}
