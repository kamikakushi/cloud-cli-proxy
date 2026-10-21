package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/zanel1u/cloud-cli-proxy/internal/agentapi"
	"github.com/zanel1u/cloud-cli-proxy/internal/store/repository"
)

// sqliteAccountStore 使用内存 SQLite 实现 AdminClaudeAccountStore。
// BeginTx 返回真正的 *sql.Tx，handler 会把 tx 传给 repository.LockClaudeAccountForDelete
// 与 repository.DeleteClaudeAccountTx——这两个函数现在直接操作 *sql.Tx。
// CreateClaudeAccount 直接转发给真实 repository 实现，让测试跑生产 SQL 路径。
type sqliteAccountStore struct {
	db       *sql.DB
	repo     *repository.Repository
	beginErr error
	beginCnt int
	beginCtx context.Context
}

func (s *sqliteAccountStore) CreateClaudeAccount(ctx context.Context, params repository.CreateClaudeAccountParams) (repository.ClaudeAccount, error) {
	return s.repo.CreateClaudeAccount(ctx, params)
}

func (s *sqliteAccountStore) UpdateClaudeAccountAPIToken(ctx context.Context, accountID, token, baseURL string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE claude_accounts SET api_token = ?, api_base_url = ? WHERE id = ?",
		token, baseURL, accountID)
	return err
}

func (s *sqliteAccountStore) BeginTx(ctx context.Context) (*sql.Tx, error) {
	s.beginCnt++
	s.beginCtx = ctx
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return s.db.BeginTx(ctx, nil)
}

// newSQLiteAccountStore 创建内存 SQLite，按 0001/0005 迁移的真实列集建表
// （仅 handler 路径需要的最小列），并预置 FK 依赖的 users 行。
func newSQLiteAccountStore(t *testing.T) *sqliteAccountStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(context.Background(), pragma); err != nil {
			t.Fatalf("pragma %s: %v", pragma, err)
		}
	}
	// 建表：users / hosts（FK 依赖）+ claude_accounts（handler 路径需要）。
	// 时间列声明为 DATETIME（与真实迁移一致）：modernc 驱动按声明类型
	// 把 TEXT 时间值转换成 time.Time，模型才能直接 Scan。
	_, err = db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id         TEXT PRIMARY KEY,
			username   TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			updated_at DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP)
		);
		CREATE TABLE IF NOT EXISTS hosts (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			created_at DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			updated_at DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP)
		);
		CREATE TABLE IF NOT EXISTS claude_accounts (
			id                     TEXT PRIMARY KEY,
			user_id                TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			host_id                TEXT REFERENCES hosts (id) ON DELETE SET NULL,
			email                  TEXT NOT NULL,
			display_name           TEXT NOT NULL DEFAULT '',
			status                 TEXT NOT NULL DEFAULT 'active',
			created_at             DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			updated_at             DATETIME NOT NULL DEFAULT (CURRENT_TIMESTAMP),
			persistent_volume_name TEXT,
			api_token              TEXT NOT NULL DEFAULT '',
			api_base_url           TEXT NOT NULL DEFAULT ''
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_claude_accounts_email ON claude_accounts (email);
		INSERT INTO users (id, username) VALUES ('u-1', 'user-one');
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return &sqliteAccountStore{db: db, repo: repository.New(db)}
}

// seedClaudeAccount 插入一条 claude_account 行（email 固定 user@example.com），
// 供 handler 删除 / email 冲突用例使用。
func (s *sqliteAccountStore) seedClaudeAccount(t *testing.T, id, volumeName string) {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO claude_accounts (id, user_id, email, display_name, status, persistent_volume_name)
		 VALUES (?, 'u-1', 'user@example.com', 'Seeded', 'active', ?)`,
		id, volumeName)
	if err != nil {
		t.Fatalf("seed claude_account: %v", err)
	}
}

func newAdminClaudeAccountsTestRouter(t *testing.T, store AdminClaudeAccountStore, events EventRecorder) (nethttp.Handler, func()) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewAdminClaudeAccountsHandler(logger, store, nil, events)
	mux := nethttp.NewServeMux()
	mux.Handle("POST /v1/admin/claude-accounts", handler.Create())
	mux.Handle("DELETE /v1/admin/claude-accounts/{accountID}", handler.Delete())
	return mux, func() {}
}

func TestAdminClaudeAccountsCreate_Success_201AuditEventAndDBRow(t *testing.T) {
	store := newSQLiteAccountStore(t)
	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO hosts (id, user_id) VALUES ('host-1', 'u-1')`); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	events := &stubEventRecorder{}

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	body := `{"user_id":"u-1","email":"new@example.com","display_name":"Primary","host_id":"host-1"}`
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/admin/claude-accounts", strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ClaudeAccount repository.ClaudeAccount `json:"claude_account"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body must be JSON: %v", err)
	}
	acct := resp.ClaudeAccount
	if acct.ID == "" {
		t.Error("created account id must be non-empty")
	}
	if acct.UserID != "u-1" || acct.Email != "new@example.com" || acct.DisplayName != "Primary" {
		t.Errorf("unexpected account fields: %+v", acct)
	}
	if acct.Status != "active" {
		t.Errorf("status must default to active, got %q", acct.Status)
	}
	if acct.HostID == nil || *acct.HostID != "host-1" {
		t.Errorf("host_id must round-trip host-1, got %v", acct.HostID)
	}
	if acct.PersistentVolumeName != nil {
		t.Errorf("persistent_volume_name must stay unassigned at creation, got %v", *acct.PersistentVolumeName)
	}

	if !events.hasType("claude_account.created") {
		t.Error("audit event claude_account.created must be emitted")
	}

	var volumeName sql.NullString
	var status string
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT status, persistent_volume_name FROM claude_accounts WHERE id = ?`, acct.ID).
		Scan(&status, &volumeName); err != nil {
		t.Fatalf("row must exist in DB: %v", err)
	}
	if status != "active" || volumeName.Valid {
		t.Errorf("DB row mismatch: status=%q volume=%v", status, volumeName)
	}
}

func TestAdminClaudeAccountsCreate_DuplicateEmail_409(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "")
	events := &stubEventRecorder{}

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	body := `{"user_id":"u-1","email":"user@example.com"}`
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/admin/claude-accounts", strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body must be JSON: %v", err)
	}
	if resp["error"] != "email already exists" {
		t.Errorf("error must be email already exists, got %v", resp["error"])
	}
	if events.hasType("claude_account.created") {
		t.Error("duplicate create must not emit claude_account.created")
	}
	var cnt int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM claude_accounts WHERE email = 'user@example.com'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("duplicate email must not create a second row, got count=%d", cnt)
	}
}

func TestAdminClaudeAccountsCreate_ValidationErrors_400(t *testing.T) {
	store := newSQLiteAccountStore(t)
	mux, _ := newAdminClaudeAccountsTestRouter(t, store, &stubEventRecorder{})

	cases := []struct {
		name string
		body string
	}{
		{"missing user_id", `{"email":"a@example.com"}`},
		{"missing email", `{"user_id":"u-1"}`},
		{"email without at", `{"user_id":"u-1","email":"not-an-email"}`},
		{"invalid json", `not-json`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(nethttp.MethodPost, "/v1/admin/claude-accounts", strings.NewReader(tc.body))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != nethttp.StatusBadRequest {
			t.Errorf("%s: want 400, got %d body=%s", tc.name, rr.Code, rr.Body.String())
		}
	}
}

func TestAdminClaudeAccountsDelete_StrictSuccess_DBDeletedAndAuditEventEmitted(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "claude-state-acct-1")
	events := &stubEventRecorder{}

	origRun := runHostAction
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		if req.Action != agentapi.ActionVolumeRemove {
			t.Fatalf("expected ActionVolumeRemove, got %q", req.Action)
		}
		if len(req.Volumes) != 1 || req.Volumes[0].Name != "claude-state-acct-1" {
			t.Fatalf("expected single volume claude-state-acct-1, got %+v", req.Volumes)
		}
		return agentapi.HostActionResponse{}, nil
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusOK {
		t.Errorf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !events.hasType("claude_account.deleted") {
		t.Error("audit event claude_account.deleted must be emitted")
	}
	// 验证行被真实删除。
	var cnt int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM claude_accounts WHERE id = 'acct-1'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Error("claude_account must be deleted from DB")
	}
}

func TestAdminClaudeAccountsDelete_StrictHostAgentFailure_RollbackAnd409WithChineseMessage(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "claude-state-acct-1")
	events := &stubEventRecorder{}

	origRun := runHostAction
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		return agentapi.HostActionResponse{}, errors.New("volume_in_use: stuck on container_xyz")
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !events.hasType("claude_account.delete_volume_rm_failed") {
		t.Error("audit event claude_account.delete_volume_rm_failed must be emitted")
	}
	// 验证行未被删除（rollback 生效）。
	var cnt int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM claude_accounts WHERE id = 'acct-1'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("claude_account must remain after rollback, got count=%d", cnt)
	}

	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body must be JSON: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "STATE_VOLUME_IN_USE_001" {
		t.Errorf("error.code must be STATE_VOLUME_IN_USE_001, got %v", errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "请先停止使用该账号的所有 host 后重试") {
		t.Errorf("error.message must contain Chinese guidance, got %q", msg)
	}
}

func TestAdminClaudeAccountsDelete_ForceTrue_DBDeletedEvenWhenRmFails(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "claude-state-acct-1")
	events := &stubEventRecorder{}

	origRun := runHostAction
	var capturedLabels map[string]string
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		capturedLabels = req.Labels
		return agentapi.HostActionResponse{}, errors.New("daemon connection refused")
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1?force=true", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusOK {
		t.Fatalf("force=true must return 200 even when rm fails, got %d body=%s", rr.Code, rr.Body.String())
	}
	if capturedLabels["force"] != "true" {
		t.Errorf("force label must be propagated to host-agent, got %v", capturedLabels)
	}
	if !events.hasType("claude_account.force_volume_rm_failed") {
		t.Error("audit event claude_account.force_volume_rm_failed must be emitted")
	}
	// 验证 row 已被删除。
	var cnt int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM claude_accounts WHERE id = 'acct-1'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Error("claude_account must be deleted (force path commits first)")
	}

	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["volume_rm"] != "failed" {
		t.Errorf("volume_rm must be \"failed\", got %v", body["volume_rm"])
	}
	if na, _ := body["next_action"].(string); !strings.Contains(na, "docker volume rm -f") {
		t.Errorf("next_action must hint docker volume rm -f, got %q", na)
	}
}

func TestAdminClaudeAccountsDelete_AccountNotFound_404(t *testing.T) {
	store := newSQLiteAccountStore(t)
	// 不 seed 任何行 → 404。
	events := &stubEventRecorder{}

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/missing", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != nethttp.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestAdminClaudeAccountsDelete_NoVolumeName_SkipsHostAgentCall(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "")
	events := &stubEventRecorder{}
	called := false
	origRun := runHostAction
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		called = true
		return agentapi.HostActionResponse{}, nil
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, events)
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != nethttp.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
	if called {
		t.Error("host-agent must NOT be called when volume_name is empty")
	}
}

func TestAdminClaudeAccountsDelete_StrictUsesTenSecondTimeout(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "")
	origRun := runHostAction
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		return agentapi.HostActionResponse{}, nil
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, &stubEventRecorder{})
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if store.beginCtx == nil {
		t.Fatal("BeginTx must be called")
	}
	deadline, ok := store.beginCtx.Deadline()
	if !ok {
		t.Fatal("strict path must have deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 10*time.Second+100*time.Millisecond || remaining < 9*time.Second {
		t.Errorf("strict timeout must be ~10s, got %v", remaining)
	}
}

func TestAdminClaudeAccountsDelete_ForceUsesThirtySecondTimeout(t *testing.T) {
	store := newSQLiteAccountStore(t)
	store.seedClaudeAccount(t, "acct-1", "")
	origRun := runHostAction
	runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
		return agentapi.HostActionResponse{}, nil
	}
	t.Cleanup(func() { runHostAction = origRun })

	mux, _ := newAdminClaudeAccountsTestRouter(t, store, &stubEventRecorder{})
	req := httptest.NewRequest(nethttp.MethodDelete, "/v1/admin/claude-accounts/acct-1?force=true", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	deadline, ok := store.beginCtx.Deadline()
	if !ok {
		t.Fatal("force path must have deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 30*time.Second+100*time.Millisecond || remaining < 29*time.Second {
		t.Errorf("force timeout must be ~30s, got %v", remaining)
	}
}

// 确保 _ 变量压制未使用导入的编译错误（repository 在非 strict/force 路径中隐式引用）。
var _ = repository.LockClaudeAccountForDelete

func TestParseForceFlag_AcceptsTrueOneYes(t *testing.T) {
	cases := map[string]bool{"true": true, "1": true, "yes": true, "false": false, "": false, "TRUE": false}
	for s, want := range cases {
		if got := parseForceFlag(s); got != want {
			t.Errorf("parseForceFlag(%q): got %v, want %v", s, got, want)
		}
	}
}
