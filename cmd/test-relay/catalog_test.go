package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCatalogRequestRejectsPrivateFields(t *testing.T) {
	_, err := parseCatalogRequest([]byte(`{"protocol":1,"catalog":"default","schema_version":1,"epoch":0,"search_model":"leaf-arctic-asymmetric-v1","installed":["a"]}`))
	if err == nil {
		t.Fatal("parseCatalogRequest() error = nil, want private-field error")
	}
}

func TestHandleUpdates(t *testing.T) {
	fixtures := mustCatalogFixtures(t)
	mux := withCatalog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}), fixtures)

	t.Run("epoch 0 redirects", func(t *testing.T) {
		rec := postUpdates(t, mux, catalogBody(0, catalogSchemaVersion, catalogSearchModel))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
		if rec.Header().Get("Location") != catalogSnapshotPath {
			t.Fatalf("Location = %q", rec.Header().Get("Location"))
		}
	})
	t.Run("epoch 1 returns delta", func(t *testing.T) {
		rec := postUpdates(t, mux, catalogBody(1, catalogSchemaVersion, catalogSearchModel))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != catalogDeltaType {
			t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte(catalogMagic)) {
			t.Fatal("delta is not a catalog envelope")
		}
	})
	t.Run("epoch 2 is current", func(t *testing.T) {
		rec := postUpdates(t, mux, catalogBody(2, catalogSchemaVersion, catalogSearchModel))
		if rec.Code != http.StatusNotModified {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotModified)
		}
	})
	t.Run("incompatible schema redirects", func(t *testing.T) {
		rec := postUpdates(t, mux, catalogBody(2, 99, catalogSearchModel))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
		}
	})
	t.Run("forbidden field is rejected", func(t *testing.T) {
		body := `{"protocol":1,"catalog":"default","schema_version":1,"epoch":0,"search_model":"leaf-arctic-asymmetric-v1","search":"secret"}`
		rec := postUpdates(t, mux, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if rec.Header().Get("X-Error-Code") != "invalid_request" {
			t.Fatalf("X-Error-Code = %q", rec.Header().Get("X-Error-Code"))
		}
	})
	t.Run("snapshot is served", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, catalogSnapshotPath, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte(catalogMagic)) {
			t.Fatal("snapshot is not a catalog envelope")
		}
	})
}

func mustCatalogFixtures(t *testing.T) *catalogFixtures {
	t.Helper()
	t.Setenv(catalogSourceDBEnv, filepath.Join(t.TempDir(), "missing.db"))
	t.Setenv(catalogFixtureDirEnv, t.TempDir())
	fixtures, err := loadCatalogFixtures()
	if err != nil {
		t.Fatalf("loadCatalogFixtures() error = %v", err)
	}
	return fixtures
}

func catalogBody(epoch int64, schema int, model string) string {
	body, err := json.Marshal(catalogRequest{
		Protocol:      catalogProtocol,
		Catalog:       catalogName,
		SchemaVersion: schema,
		Epoch:         epoch,
		SearchModel:   model,
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func postUpdates(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/updates", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestBuildCatalogFixturesWithoutSource(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required to build catalog fixtures")
	}
	dir := t.TempDir()
	t.Setenv(catalogSourceDBEnv, filepath.Join(dir, "missing.db"))
	t.Setenv(catalogFixtureDirEnv, dir)
	fixtures, err := loadCatalogFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures.snapshot) == 0 || len(fixtures.delta) == 0 {
		t.Fatal("fixtures are empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshot.zcat")); !os.IsNotExist(err) {
		t.Fatal("stub snapshot should not be cached")
	}
}

func TestImportRelayCatalogProjectsAppsReleasesAndAssets(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required to import catalog events")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "src.db")
	dest := filepath.Join(dir, "dest.db")
	appID := strings.Repeat("aa", 32)
	releaseID := strings.Repeat("bb", 32)
	assetID := strings.Repeat("cc", 32)
	pubkey := strings.Repeat("dd", 32)
	if err := runSQLite(source, fmt.Sprintf(`
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    pubkey TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    kind INTEGER NOT NULL,
    tags JSONB NOT NULL,
    content TEXT NOT NULL,
    sig TEXT NOT NULL
);
CREATE TABLE tags (
    event_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (key, value, event_id)
);
INSERT INTO events(id, pubkey, created_at, kind, tags, content, sig) VALUES
  ('%s', '%s', 1, 32267, '[["d","dev.one"],["name","One"]]', '', '00'),
  ('%s', '%s', 2, 30063, '[["d","dev.one@1.0"],["i","dev.one"],["version","1.0"],["c","stable"]]', '', '00'),
  ('%s', '%s', 3, 3063, '[["i","dev.one"],["version","1.0"],["m","application/vnd.android.package-archive"]]', '', '00');
`, appID, pubkey, releaseID, pubkey, assetID, pubkey)); err != nil {
		t.Fatal(err)
	}
	if err := runSQLite(dest, compactSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := importRelayCatalog(dest, source); err != nil {
		t.Fatal(err)
	}
	events, err := countSQLite(dest, "SELECT COUNT(*) FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	apps, err := countSQLite(dest, "SELECT COUNT(*) FROM apps;")
	if err != nil {
		t.Fatal(err)
	}
	releases, err := countSQLite(dest, "SELECT COUNT(*) FROM releases;")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := countSQLite(dest, "SELECT COUNT(*) FROM assets;")
	if err != nil {
		t.Fatal(err)
	}
	if events != 3 || apps != 1 || releases != 1 || assets != 1 {
		t.Fatalf("imported events=%d apps=%d releases=%d assets=%d", events, apps, releases, assets)
	}
}

func TestImportRelayCatalogSynthesizesReleaseFromStandaloneAsset(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required to import catalog events")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "src.db")
	dest := filepath.Join(dir, "dest.db")
	appID := strings.Repeat("aa", 32)
	assetID := strings.Repeat("cc", 32)
	pubkey := strings.Repeat("dd", 32)
	if err := runSQLite(source, fmt.Sprintf(`
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    pubkey TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    kind INTEGER NOT NULL,
    tags JSONB NOT NULL,
    content TEXT NOT NULL,
    sig TEXT NOT NULL
);
CREATE TABLE tags (
    event_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (key, value, event_id)
);
INSERT INTO events(id, pubkey, created_at, kind, tags, content, sig) VALUES
  ('%s', '%s', 1, 32267, '[["d","dev.one"],["name","One"]]', '', '00'),
  ('%s', '%s', 3, 3063, '[["i","dev.one"],["version","1.0"],["m","application/vnd.android.package-archive"]]', 'Fixed a crash', '00');
`, appID, pubkey, assetID, pubkey)); err != nil {
		t.Fatal(err)
	}
	if err := runSQLite(dest, compactSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := importRelayCatalog(dest, source); err != nil {
		t.Fatal(err)
	}
	events, err := countSQLite(dest, "SELECT COUNT(*) FROM events;")
	if err != nil {
		t.Fatal(err)
	}
	apps, err := countSQLite(dest, "SELECT COUNT(*) FROM apps;")
	if err != nil {
		t.Fatal(err)
	}
	releases, err := countSQLite(dest, "SELECT COUNT(*) FROM releases;")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := countSQLite(dest, "SELECT COUNT(*) FROM assets;")
	if err != nil {
		t.Fatal(err)
	}
	if events != 2 || apps != 1 || releases != 1 || assets != 1 {
		t.Fatalf("imported events=%d apps=%d releases=%d assets=%d", events, apps, releases, assets)
	}
}
