package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	catalogMagic         = "ZSC1"
	catalogProtocol      = 1
	catalogName          = "default"
	catalogSchemaVersion = 1
	catalogSearchModel   = "leaf-arctic-asymmetric-v1"
	catalogManifestKind  = 31_267
	catalogDeltaType     = "application/vnd.zapstore.catalog-delta"
	catalogUpdatesLimit  = 1 << 20
	catalogSnapshotPath  = "/catalog/snapshot"
	catalogPrivateKeyEnv = "CATALOG_SIGNING_KEY"
	catalogSourceDBEnv   = "CATALOG_SOURCE_DB"
	catalogFixtureDirEnv = "CATALOG_FIXTURE_DIR"
	catalogBundleDBEnv   = "CATALOG_BUNDLE_DB"
)

var (
	errCatalogBadRequest = errors.New("invalid catalog request")
	forbiddenUpdateKeys  = []string{"installed", "apps", "package", "packages", "search", "query", "hashes", "versions"}
)

type catalogFixtures struct {
	pubkey   string
	snapshot []byte
	delta    []byte
}

type catalogRequest struct {
	Protocol      int    `json:"protocol"`
	Catalog       string `json:"catalog"`
	SchemaVersion int    `json:"schema_version"`
	Epoch         int64  `json:"epoch"`
	SearchModel   string `json:"search_model"`
}

func loadCatalogFixtures() (*catalogFixtures, error) {
	key := strings.TrimSpace(os.Getenv(catalogPrivateKeyEnv))
	if key == "" {
		key = strings.Repeat("0", 63) + "1"
	}
	pubkey, err := nostr.GetPublicKey(key)
	if err != nil {
		return nil, fmt.Errorf("catalog signing key: %w", err)
	}
	dir := strings.TrimSpace(os.Getenv(catalogFixtureDirEnv))
	if dir == "" {
		dir = filepath.Join("testdata", "catalog")
	}
	source := catalogSourcePath()
	snapshotPath := filepath.Join(dir, "snapshot.zcat")
	deltaPath := filepath.Join(dir, "delta.zcat")
	if snapshot, err := os.ReadFile(snapshotPath); err == nil && !staleCatalogFixture(snapshot, source) {
		delta, err := os.ReadFile(deltaPath)
		if err != nil {
			return nil, fmt.Errorf("read catalog delta fixture: %w", err)
		}
		slog.Info("loaded cached catalog fixtures", "source", source, "snapshot_bytes", len(snapshot), "delta_bytes", len(delta))
		return &catalogFixtures{pubkey: pubkey, snapshot: snapshot, delta: delta}, nil
	}
	fixtures, err := buildCatalogFixtures(key, pubkey, source)
	if err != nil {
		return nil, err
	}
	if source != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.WriteFile(snapshotPath, fixtures.snapshot, 0o644)
			_ = os.WriteFile(deltaPath, fixtures.delta, 0o644)
		}
	}
	slog.Info("loaded catalog fixtures", "source", source, "snapshot_bytes", len(fixtures.snapshot), "delta_bytes", len(fixtures.delta))
	return fixtures, nil
}

func withCatalog(next http.Handler, fixtures *catalogFixtures) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimRight(r.URL.Path, "/")
		switch {
		case path == "/updates":
			handleUpdates(w, r, fixtures)
		case path == catalogSnapshotPath:
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(fixtures.snapshot)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func handleUpdates(w http.ResponseWriter, r *http.Request, fixtures *catalogFixtures) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, catalogUpdatesLimit+1))
	if err != nil || len(body) > catalogUpdatesLimit {
		writeCatalogError(w, http.StatusBadRequest, "invalid_body", "request body is invalid")
		return
	}
	req, err := parseCatalogRequest(body)
	if err != nil {
		writeCatalogError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Protocol != catalogProtocol || req.Catalog != catalogName {
		writeCatalogError(w, http.StatusBadRequest, "unsupported_catalog", "catalog request is unsupported")
		return
	}
	if req.SchemaVersion != catalogSchemaVersion || req.SearchModel != catalogSearchModel {
		redirectSnapshot(w, r)
		return
	}
	switch req.Epoch {
	case 0:
		redirectSnapshot(w, r)
	case 1:
		w.Header().Set("Content-Type", catalogDeltaType)
		_, _ = w.Write(fixtures.delta)
	case 2:
		w.WriteHeader(http.StatusNotModified)
	default:
		redirectSnapshot(w, r)
	}
}

func parseCatalogRequest(body []byte) (catalogRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return catalogRequest{}, errCatalogBadRequest
	}
	for _, key := range forbiddenUpdateKeys {
		if _, ok := raw[key]; ok {
			return catalogRequest{}, fmt.Errorf("request must not include %s", key)
		}
	}
	var req catalogRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return catalogRequest{}, errCatalogBadRequest
	}
	if req.Catalog == "" || req.SearchModel == "" {
		return catalogRequest{}, errCatalogBadRequest
	}
	return req, nil
}

func redirectSnapshot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, catalogSnapshotPath, http.StatusSeeOther)
}

func writeCatalogError(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("X-Error-Code", code)
	w.Header().Set("X-Reason", reason)
	w.WriteHeader(status)
}

func buildCatalogFixtures(privateKey, pubkey, sourceDB string) (*catalogFixtures, error) {
	dir, err := os.MkdirTemp("", "zapstore-catalog-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	snapshotDB := filepath.Join(dir, "epoch2.db")
	epoch1DB := filepath.Join(dir, "epoch1.db")
	if err := createCompactDatabase(snapshotDB, sourceDB, true); err != nil {
		return nil, err
	}
	if sourceDB != "" {
		count, err := countSQLite(snapshotDB, "SELECT COUNT(*) FROM events;")
		if err != nil {
			return nil, err
		}
		if count < 1000 {
			return nil, fmt.Errorf("imported %d catalog events from %s, expected the full relay snapshot", count, sourceDB)
		}
	}
	if err := createCompactDatabase(epoch1DB, sourceDB, false); err != nil {
		return nil, err
	}
	if err := setCatalogState(snapshotDB, 2); err != nil {
		return nil, err
	}
	if err := setCatalogState(epoch1DB, 1); err != nil {
		return nil, err
	}
	if sourceDB != "" {
		if err := persistCompactCatalog(snapshotDB); err != nil {
			return nil, err
		}
	}
	snapshotBytes, err := os.ReadFile(snapshotDB)
	if err != nil {
		return nil, err
	}
	snapshot, err := encodeCatalogEnvelope(privateKey, pubkey, "snapshot", 0, 2, snapshotBytes)
	if err != nil {
		return nil, err
	}
	deltaJSON, err := buildDeltaPayload(privateKey)
	if err != nil {
		return nil, err
	}
	delta, err := encodeCatalogEnvelope(privateKey, pubkey, "delta", 1, 2, deltaJSON)
	if err != nil {
		return nil, err
	}
	return &catalogFixtures{pubkey: pubkey, snapshot: snapshot, delta: delta}, nil
}

func createCompactDatabase(dest, source string, includeDeltaApp bool) error {
	if err := runSQLite(dest, compactSchemaSQL); err != nil {
		return err
	}
	if _, err := os.Stat(source); err == nil {
		if err := importRelayCatalog(dest, source); err != nil {
			return err
		}
	}
	if includeDeltaApp {
		return insertSyntheticApp(dest, "dev.zapstore.catalogtest", "Catalog Test", 2)
	}
	return insertSyntheticApp(dest, "dev.zapstore.catalogtest", "Catalog Test", 1)
}

func importRelayCatalog(dest, source string) error {
	query := `
ATTACH DATABASE '` + escapeSQLitePath(source) + `' AS src;
INSERT OR IGNORE INTO events(id, pubkey, created_at, kind, d_tag, content, tags)
SELECT
  unhex(id),
  unhex(pubkey),
  created_at,
  kind,
  COALESCE((
    SELECT json_extract(value, '$[1]') FROM json_each(src.events.tags)
    WHERE json_extract(value, '$[0]') = 'd' LIMIT 1
  ), ''),
  content,
  tags
FROM src.events
WHERE kind IN (32267, 30063, 3063, 30267);
INSERT OR IGNORE INTO event_tags(event_id, key, value)
SELECT e.id, json_extract(j.value, '$[0]'), json_extract(j.value, '$[1]')
FROM events e, json_each(e.tags) j
WHERE json_type(j.value) = 'array' AND json_array_length(j.value) > 1;
INSERT OR REPLACE INTO apps(app_id, event_id, pubkey, name, created_at)
SELECT
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'd' LIMIT 1),
  e.id,
  e.pubkey,
  COALESCE((SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'name' LIMIT 1), ''),
  e.created_at
FROM events e WHERE e.kind = 32267
  AND (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'd' LIMIT 1) IS NOT NULL;
INSERT OR REPLACE INTO releases(app_id, version, event_id, channel, created_at)
SELECT
  COALESCE(
    (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1),
    CASE WHEN instr(e.d_tag, '@') > 0 THEN substr(e.d_tag, 1, instr(e.d_tag, '@') - 1) ELSE e.d_tag END
  ),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version' LIMIT 1),
  e.id,
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'c' LIMIT 1),
  e.created_at
FROM events e WHERE e.kind = 30063
  AND COALESCE(
    (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1),
    e.d_tag
  ) != ''
  AND (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version' LIMIT 1) IS NOT NULL;
INSERT OR REPLACE INTO assets(event_id, app_id, version_code, version, mime, platform, variant, certificate_hash, file_hash, url, created_at)
SELECT
  e.id,
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1),
  (SELECT CAST(json_extract(value, '$[1]') AS INTEGER) FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version_code' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'm' LIMIT 1),
  (SELECT group_concat(json_extract(value, '$[1]'), ',') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'f'),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'variant' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'apk_certificate_hash' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'x' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'url' LIMIT 1),
  e.created_at
FROM events e WHERE e.kind = 3063
  AND (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1) IS NOT NULL;
INSERT OR IGNORE INTO releases(app_id, version, event_id, channel, created_at)
SELECT
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1),
  (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version' LIMIT 1),
  e.id,
  NULL,
  e.created_at
FROM events e WHERE e.kind = 3063
  AND (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'i' LIMIT 1) IS NOT NULL
  AND (SELECT json_extract(value, '$[1]') FROM json_each(e.tags) WHERE json_extract(value, '$[0]') = 'version' LIMIT 1) IS NOT NULL;
DETACH DATABASE src;
`
	return runSQLite(dest, query)
}

func insertSyntheticApp(dest, appID, name string, epoch int64) error {
	return runSQLite(dest, fmt.Sprintf(`
INSERT OR REPLACE INTO catalog_state(catalog, epoch, schema_version, search_model, generation)
VALUES ('%s', %d, %d, '%s', 0);
INSERT OR REPLACE INTO apps(app_id, event_id, pubkey, name, created_at)
VALUES ('%s', X'%s', X'%s', '%s', 1750000000);
`, catalogName, epoch, catalogSchemaVersion, catalogSearchModel, appID, strings.Repeat("ab", 16), strings.Repeat("cd", 16), name))
}

func setCatalogState(dest string, epoch int64) error {
	return runSQLite(dest, fmt.Sprintf(
		"INSERT OR REPLACE INTO catalog_state(catalog, epoch, schema_version, search_model, generation) VALUES ('%s', %d, %d, '%s', 0);",
		catalogName, epoch, catalogSchemaVersion, catalogSearchModel,
	))
}

func buildDeltaPayload(privateKey string) ([]byte, error) {
	event := &nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      32267,
		Tags: nostr.Tags{
			{"d", "dev.zapstore.catalogtest"},
			{"name", "Catalog Test"},
			{"summary", "Updated in epoch 2"},
		},
		Content: "updated",
	}
	if err := event.Sign(privateKey); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"events":      []any{event},
		"deleted_ids": []string{},
		"old_epoch":   1,
		"new_epoch":   2,
	}
	return json.Marshal(payload)
}

func encodeCatalogEnvelope(privateKey, pubkey, kind string, oldEpoch, newEpoch int64, payload []byte) ([]byte, error) {
	sum := sha256.Sum256(payload)
	manifest := &nostr.Event{
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      catalogManifestKind,
		Tags:      nostr.Tags{{"d", catalogName}},
		Content: string(mustJSON(map[string]any{
			"content_hash":   hex.EncodeToString(sum[:]),
			"old_epoch":      oldEpoch,
			"new_epoch":      newEpoch,
			"schema_version": catalogSchemaVersion,
			"search_model":   catalogSearchModel,
			"catalog":        catalogName,
			"kind":           kind,
		})),
	}
	if err := manifest.Sign(privateKey); err != nil {
		return nil, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	header := make([]byte, 8)
	copy(header, catalogMagic)
	binary.BigEndian.PutUint32(header[4:], uint32(len(manifestJSON)))
	out := append(header, manifestJSON...)
	out = append(out, compressed.Bytes()...)
	_ = pubkey
	return out, nil
}

func runSQLite(db, sql string) error {
	cmd := exec.Command("sqlite3", db)
	cmd.Stdin = strings.NewReader(sql)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite3 %s: %w: %s", db, err, output)
	}
	return nil
}

func escapeSQLitePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return strings.ReplaceAll(filepath.ToSlash(abs), "'", "''")
}

func catalogSourcePath() string {
	if value, ok := os.LookupEnv(catalogSourceDBEnv); ok {
		path := strings.TrimSpace(value)
		if path == "" {
			return ""
		}
		if fileExists(path) {
			return path
		}
		return ""
	}
	var starts []string
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(thisFile))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	relatives := []string{
		filepath.Join("android", "relay_2026-09-03.db"),
		filepath.Join("zapstore-v2", "android", "relay_2026-09-03.db"),
	}
	seen := map[string]struct{}{}
	for _, start := range starts {
		dir := start
		for range 8 {
			for _, rel := range relatives {
				candidate := filepath.Clean(filepath.Join(dir, rel))
				if _, dup := seen[candidate]; dup {
					continue
				}
				seen[candidate] = struct{}{}
				if fileExists(candidate) {
					return candidate
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

func persistCompactCatalog(snapshotDB string) error {
	for _, dest := range catalogBundleDestinations() {
		if dest == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("create catalog bundle dir: %w", err)
		}
		if err := copyFile(snapshotDB, dest); err != nil {
			return fmt.Errorf("write compact catalog %s: %w", dest, err)
		}
		slog.Info("wrote compact catalog", "path", dest)
	}
	return nil
}

func catalogBundleDestinations() []string {
	var dests []string
	if env := strings.TrimSpace(os.Getenv(catalogBundleDBEnv)); env != "" {
		dests = append(dests, env)
	}
	if dir := strings.TrimSpace(os.Getenv(catalogFixtureDirEnv)); dir != "" {
		dests = append(dests, filepath.Join(dir, "catalog.db"))
	} else {
		dests = append(dests, filepath.Join("testdata", "catalog", "catalog.db"))
	}
	if env := strings.TrimSpace(os.Getenv(catalogBundleDBEnv)); env == "" {
		if bundle := androidAssetsCatalogDB(); bundle != "" {
			dests = append(dests, bundle)
		}
	}
	return uniqueStrings(dests)
}

func androidAssetsCatalogDB() string {
	source := catalogSourcePath()
	if source == "" || filepath.Base(source) != "relay_2026-09-03.db" {
		return ""
	}
	return filepath.Join(filepath.Dir(source), "src", "main", "assets", "catalog.db")
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func staleCatalogFixture(snapshot []byte, source string) bool {
	return source != "" && len(snapshot) < 100_000
}

func countSQLite(db, query string) (int, error) {
	cmd := exec.Command("sqlite3", db, query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("sqlite3 %s: %w: %s", db, err, output)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		return 0, fmt.Errorf("parse sqlite count %q: %w", output, err)
	}
	return count, nil
}

func mustJSON(value any) []byte {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return bytes
}

const compactSchemaSQL = `
CREATE TABLE IF NOT EXISTS events (
    id BLOB PRIMARY KEY,
    pubkey BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    kind INTEGER NOT NULL,
    d_tag TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    tags TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_kind_time ON events(kind, created_at DESC, id);
CREATE INDEX IF NOT EXISTS events_pubkey_kind_time ON events(pubkey, kind, created_at DESC, id);
CREATE INDEX IF NOT EXISTS events_kind_d ON events(kind, pubkey, d_tag);
CREATE TABLE IF NOT EXISTS event_tags (
    event_id BLOB NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (key, value, event_id),
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS event_tags_event ON event_tags(event_id);
CREATE TABLE IF NOT EXISTS catalog_state (
    catalog TEXT PRIMARY KEY,
    epoch INTEGER NOT NULL,
    schema_version INTEGER NOT NULL,
    search_model TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS apps (
    app_id TEXT PRIMARY KEY,
    event_id BLOB NOT NULL,
    pubkey BLOB NOT NULL,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS releases (
    app_id TEXT NOT NULL,
    version TEXT NOT NULL,
    event_id BLOB NOT NULL,
    channel TEXT,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (app_id, version)
);
CREATE TABLE IF NOT EXISTS assets (
    event_id BLOB PRIMARY KEY,
    app_id TEXT NOT NULL,
    version_code INTEGER,
    version TEXT,
    mime TEXT,
    platform TEXT,
    variant TEXT,
    certificate_hash TEXT,
    file_hash TEXT,
    url TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_app ON assets(app_id, version_code);
INSERT OR REPLACE INTO catalog_state(catalog, epoch, schema_version, search_model, generation)
VALUES ('default', 0, 1, 'leaf-arctic-asymmetric-v1', 0);
`
