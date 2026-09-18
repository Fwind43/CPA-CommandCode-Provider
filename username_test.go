package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsernameFileNameAndMetadata(t *testing.T) {
	for _, field := range []string{"userName", "username", "user_name"} {
		raw := map[string]any{field: "alice", "userId": "account-a", "apiKey": testKeyA}
		name := commandCodeFileName(raw)
		if !strings.HasPrefix(name, "commandcode-alice-") || !strings.HasSuffix(name, ".json") {
			t.Fatalf("name=%s", name)
		}
		if commandCodeFileName(raw) != name {
			t.Fatal("unstable name")
		}
		auth := authDataFromCredential(raw, name)
		if auth.Label != "alice" || auth.Metadata["username"] != "alice" {
			t.Fatalf("missing username: %v", auth.Metadata)
		}
		raw["userId"] = "account-b"
		if commandCodeFileName(raw) == name {
			t.Fatal("same-name accounts collide")
		}
	}
	raw := map[string]any{"username": "../../a/b\\c", "userId": "id"}
	name := commandCodeFileName(raw)
	if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		t.Fatalf("unsafe filename %s", name)
	}
}

func TestDashboardUsernameAllowlist(t *testing.T) {
	dir := t.TempDir()
	startQuotaTestServer(t, dir)
	for _, field := range []string{"userName", "username", "user_name"} {
		data, _ := json.Marshal(map[string]any{field: "Alice", "apiKey": testKeyA, "private": "do-not-expose"})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("commandcode-%d.json", len(field)*100+int(field[4]))), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	accounts := quotaAccounts()
	if len(accounts) != 3 {
		t.Fatalf("accounts=%d", len(accounts))
	}
	for _, account := range accounts {
		if account.Label != "Alice" || account.Username != "Alice" {
			t.Fatalf("missing username: %v", account)
		}
	}
	encoded, _ := json.Marshal(accounts)
	for _, secret := range []string{testKeyA, dir, "do-not-expose"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private data leaked")
		}
	}
}

func TestDashboardEmailAndStorage(t *testing.T) {
	dir := t.TempDir()
	startQuotaTestServer(t, dir)
	for i, field := range []string{"email", "userEmail", "user_email"} {
		raw := map[string]any{"userName": "Alice", field: "alice@example.com", "apiKey": testKeyA}
		auth := authDataFromCredential(raw, "commandcode-test.json")
		var stored map[string]any
		if err := json.Unmarshal(auth.StorageJSON, &stored); err != nil {
			t.Fatal(err)
		}
		if stored["email"] != "alice@example.com" || auth.Metadata["email"] != "alice@example.com" {
			t.Fatal("email not preserved")
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("commandcode-email-%d.json", i)), auth.StorageJSON, 0600); err != nil {
			t.Fatal(err)
		}
	}
	accounts := quotaAccounts()
	if len(accounts) != 3 {
		t.Fatalf("accounts=%d", len(accounts))
	}
	for _, a := range accounts {
		if a.Username != "Alice" || a.Email != "alice@example.com" {
			t.Fatalf("missing identity: %v", a)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "commandcode-no-email.json"), []byte(`{"userName":"Bob"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, a := range quotaAccounts() {
		if a.Username == "Bob" {
			data, _ := json.Marshal(a)
			if strings.Contains(string(data), `"email"`) {
				t.Fatal("empty email should be omitted")
			}
		}
	}
	if !strings.Contains(quotaDashboardHTML, "if(a.email)root.append(node('div',a.email,'email'))") {
		t.Fatal("email display missing")
	}
}
