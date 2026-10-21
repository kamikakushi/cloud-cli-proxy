package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	nethttp "net/http"
	"strings"
	"time"

	"database/sql"

	"github.com/zanel1u/cloud-cli-proxy/internal/agentapi"
	"github.com/zanel1u/cloud-cli-proxy/internal/store/repository"
)

// AdminClaudeAccountStore 暴露 Plan 02 仅需的最小集（与 AdminHostStore 风格一致）。
type AdminClaudeAccountStore interface {
	BeginTx(ctx context.Context) (*sql.Tx, error)
	// CreateClaudeAccount 新建 claude_account；id 由 store 层生成（uuid v4），
	// email 撞 UNIQUE 约束时返回 SQLite 原始错误（handler 转 409）。
	CreateClaudeAccount(ctx context.Context, params repository.CreateClaudeAccountParams) (repository.ClaudeAccount, error)
	// UpdateClaudeAccountAPIToken 设置账号的容器侧 API 凭据（migration 0005）。
	UpdateClaudeAccountAPIToken(ctx context.Context, accountID, token, baseURL string) error
}

// HostActionRunner 抽象 host-agent 调用，让 handler 在 embedded（in-process worker）和
// 远端 host-agent 两种部署模式下都能工作。
//   - 远端模式：*agentapi.Client.RunHostAction 直接满足
//   - embedded 模式：用 EmbeddedHostActionRunner 适配 EmbeddedDispatcher.Dispatch（语义等价）
type HostActionRunner interface {
	RunHostAction(ctx context.Context, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error)
}

// runHostAction 包级 var 便于测试注入 mock（沿用 syncContainerPassword 模式 admin_hosts.go）。
var runHostAction = func(ctx context.Context, client HostActionRunner, req agentapi.HostActionRequest) (agentapi.HostActionResponse, error) {
	return client.RunHostAction(ctx, req)
}

type AdminClaudeAccountsHandler struct {
	logger      *slog.Logger
	store       AdminClaudeAccountStore
	agentClient HostActionRunner
	events      EventRecorder
}

func NewAdminClaudeAccountsHandler(logger *slog.Logger, store AdminClaudeAccountStore, agentClient HostActionRunner, events EventRecorder) *AdminClaudeAccountsHandler {
	return &AdminClaudeAccountsHandler{logger: logger, store: store, agentClient: agentClient, events: events}
}

func (h *AdminClaudeAccountsHandler) Delete() nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		accountID := r.PathValue("accountID")
		if parseForceFlag(r.URL.Query().Get("force")) {
			h.deleteForce(w, r, accountID)
			return
		}
		h.deleteStrict(w, r, accountID)
	})
}

type setAPITokenRequest struct {
	// APIToken 支持 Anthropic 官方 key（sk-ant-api*，注入 ANTHROPIC_API_KEY）
	// 或网关 Bearer token（注入 ANTHROPIC_AUTH_TOKEN）；空串清除。
	APIToken string `json:"api_token"`
	// APIBaseURL 可选中转网关，注入 ANTHROPIC_BASE_URL；空串清除。
	APIBaseURL string `json:"api_base_url"`
}

type createClaudeAccountRequest struct {
	// UserID 必填：账号归属的 users.id。
	UserID string `json:"user_id"`
	// Email 必填且需含 @：全表 UNIQUE，冲突返回 409。
	Email string `json:"email"`
	// DisplayName 可选展示名。
	DisplayName string `json:"display_name"`
	// HostID 可选：创建时直接绑定宿主机；为空表示稍后绑定。
	HostID *string `json:"host_id"`
}

// Create 新建 claude_account（补齐上游 Phase 33 缺失的创建入口）。
// POST /v1/admin/claude-accounts {user_id, email, display_name?, host_id?}
// 成功 201 返回新账号；email 重复 409；user_id/host_id 不存在（SQLite
// FOREIGN KEY 约束兜底）400。响应不含 api_token 明文（模型本身就不暴露该列）。
func (h *AdminClaudeAccountsHandler) Create() nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		var req createClaudeAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		req.UserID = strings.TrimSpace(req.UserID)
		req.Email = strings.TrimSpace(req.Email)
		req.DisplayName = strings.TrimSpace(req.DisplayName)
		if req.UserID == "" {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "user_id is required"})
			return
		}
		if req.Email == "" || !strings.Contains(req.Email, "@") {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "email is required and must contain @"})
			return
		}
		var hostID *string
		if req.HostID != nil {
			trimmed := strings.TrimSpace(*req.HostID)
			if trimmed != "" {
				hostID = &trimmed
			}
		}

		account, err := h.store.CreateClaudeAccount(r.Context(), repository.CreateClaudeAccountParams{
			UserID:      req.UserID,
			Email:       req.Email,
			DisplayName: req.DisplayName,
			HostID:      hostID,
		})
		if err != nil {
			errMsg := err.Error()
			if isUniqueViolation(err) || strings.Contains(errMsg, "unique") || strings.Contains(errMsg, "duplicate") {
				writeJSON(w, nethttp.StatusConflict, map[string]string{"error": "email already exists"})
				return
			}
			if strings.Contains(errMsg, "FOREIGN KEY constraint failed") {
				writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "user_id or host_id does not exist"})
				return
			}
			h.logger.Error("create claude_account failed", "user_id", req.UserID, "email", req.Email, "error", err)
			writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "create claude_account failed"})
			return
		}

		var eventHostID any
		if account.HostID != nil {
			eventHostID = *account.HostID
		}
		h.recordEvent(r.Context(), "claude_account.created", "管理员创建 Claude 账号", map[string]any{
			"account_id": account.ID,
			"user_id":    account.UserID,
			"email":      account.Email,
			"host_id":    eventHostID,
		})

		writeJSON(w, nethttp.StatusCreated, map[string]any{"claude_account": account})
	})
}

// SetAPIToken 设置账号的容器侧 API 凭据。对已绑定的运行中主机不实时下发：
// 环境变量属 docker create 参数，变更需重建主机生效（响应中注明）。
func (h *AdminClaudeAccountsHandler) SetAPIToken() nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		accountID := r.PathValue("accountID")

		var req setAPITokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		req.APIToken = strings.TrimSpace(req.APIToken)
		req.APIBaseURL = strings.TrimSpace(req.APIBaseURL)
		if req.APIBaseURL != "" && req.APIToken == "" {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "api_base_url requires api_token"})
			return
		}
		if req.APIBaseURL != "" && !strings.HasPrefix(req.APIBaseURL, "http://") && !strings.HasPrefix(req.APIBaseURL, "https://") {
			writeJSON(w, nethttp.StatusBadRequest, map[string]string{"error": "api_base_url must start with http:// or https://"})
			return
		}

		if err := h.store.UpdateClaudeAccountAPIToken(r.Context(), accountID, req.APIToken, req.APIBaseURL); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, nethttp.StatusNotFound, map[string]string{"error": "claude_account not found"})
				return
			}
			h.logger.Error("set claude_account api token failed", "account_id", accountID, "error", err)
			writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "set api token failed"})
			return
		}

		h.recordEvent(r.Context(), "claude_account.api_token_set", "管理员设置 Claude 账号 API 凭据", map[string]any{
			"account_id":   accountID,
			"has_token":    req.APIToken != "",
			"has_base_url": req.APIBaseURL != "",
			"key_flavor":   map[bool]string{true: "api_key", false: "none"}[strings.HasPrefix(req.APIToken, "sk-ant-api")],
		})

		writeJSON(w, nethttp.StatusOK, map[string]any{
			"account_id":   accountID,
			"has_token":    req.APIToken != "",
			"has_base_url": req.APIBaseURL != "",
			"note":         "环境变量在容器创建时注入；已存在的宿主机需 rebuild 后生效",
		})
	})
}

func parseForceFlag(s string) bool {
	switch s {
	case "true", "1", "yes":
		return true
	}
	return false
}

// deleteStrict D-18 强一致路径：BEGIN → SELECT FOR UPDATE → 调 host-agent → 成功 DELETE+COMMIT；失败 ROLLBACK + 409。
func (h *AdminClaudeAccountsHandler) deleteStrict(w nethttp.ResponseWriter, r *nethttp.Request, accountID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tx, err := h.store.BeginTx(ctx)
	if err != nil {
		h.logger.Error("begin tx failed", "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "begin tx failed"})
		return
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	id, volumeName, err := repository.LockClaudeAccountForDelete(ctx, tx, accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, nethttp.StatusNotFound, map[string]string{"error": "claude_account not found"})
			return
		}
		h.logger.Error("lock claude_account failed", "id", accountID, "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "lock claude_account failed"})
		return
	}

	if volumeName != "" {
		req := agentapi.HostActionRequest{
			Action:  agentapi.ActionVolumeRemove,
			Volumes: []agentapi.VolumeMount{{Name: volumeName}},
		}
		if _, err := runHostAction(ctx, h.agentClient, req); err != nil {
			h.recordEvent(r.Context(), "claude_account.delete_volume_rm_failed", "管理员删除 Claude 账号", map[string]any{
				"account_id":    id,
				"volume_name":   volumeName,
				"error_code":    "volume_in_use",
				"error_message": err.Error(),
			})
			writeJSON(w, nethttp.StatusConflict, map[string]any{
				"error": map[string]string{
					"code":        "STATE_VOLUME_IN_USE_001",
					"message":     "请先停止使用该账号的所有 host 后重试，或追加 ?force=true 强删 volume",
					"next_action": "停止 host → 重试 DELETE，或附加 ?force=true",
				},
			})
			return
		}
	}

	if err := repository.DeleteClaudeAccountTx(ctx, tx, id); err != nil {
		h.logger.Error("delete claude_account failed", "id", id, "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "delete claude_account failed"})
		return
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("commit failed", "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "commit failed"})
		return
	}
	rollback = false

	h.recordEvent(r.Context(), "claude_account.deleted", "管理员删除 Claude 账号", map[string]any{
		"account_id":  id,
		"volume_name": volumeName,
		"force":       false,
	})
	writeJSON(w, nethttp.StatusOK, map[string]any{
		"deleted":   true,
		"volume_rm": "succeeded",
	})
}

// deleteForce D-19 最终一致路径：DB 先 COMMIT；rm 失败仅写 audit + 返回 200。
func (h *AdminClaudeAccountsHandler) deleteForce(w nethttp.ResponseWriter, r *nethttp.Request, accountID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tx, err := h.store.BeginTx(ctx)
	if err != nil {
		h.logger.Error("begin tx failed (force)", "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "begin tx failed"})
		return
	}
	id, volumeName, err := repository.LockClaudeAccountForDelete(ctx, tx, accountID)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, nethttp.StatusNotFound, map[string]string{"error": "claude_account not found"})
			return
		}
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "lock claude_account failed"})
		return
	}
	if err := repository.DeleteClaudeAccountTx(ctx, tx, id); err != nil {
		_ = tx.Rollback()
		h.logger.Error("delete claude_account failed (force)", "id", id, "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "delete claude_account failed"})
		return
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("commit failed (force)", "error", err)
		writeJSON(w, nethttp.StatusInternalServerError, map[string]string{"error": "commit failed"})
		return
	}

	resp := map[string]any{"deleted": true, "volume_rm": "skipped"}
	if volumeName != "" {
		req := agentapi.HostActionRequest{
			Action:  agentapi.ActionVolumeRemove,
			Volumes: []agentapi.VolumeMount{{Name: volumeName}},
			Labels:  map[string]string{"force": "true"},
		}
		if _, err := runHostAction(ctx, h.agentClient, req); err != nil {
			h.recordEvent(r.Context(), "claude_account.force_volume_rm_failed", "管理员删除 Claude 账号", map[string]any{
				"account_id":    id,
				"volume_name":   volumeName,
				"error_message": err.Error(),
			})
			resp["volume_rm"] = "failed"
			resp["next_action"] = "运维需手工 docker volume rm -f " + volumeName
		} else {
			resp["volume_rm"] = "succeeded"
		}
	}
	h.recordEvent(r.Context(), "claude_account.deleted", "管理员删除 Claude 账号", map[string]any{
		"account_id":  id,
		"volume_name": volumeName,
		"force":       true,
	})
	writeJSON(w, nethttp.StatusOK, resp)
}

func (h *AdminClaudeAccountsHandler) recordEvent(ctx context.Context, eventType, message string, metadata map[string]any) {
	if h.events == nil {
		return
	}
	if _, err := h.events.RecordEvent(ctx, repository.RecordEventParams{
		Level:    "info",
		Type:     eventType,
		Message:  message,
		Metadata: metadata,
	}); err != nil {
		h.logger.Error("record event failed", "type", eventType, "error", err)
	}
}
