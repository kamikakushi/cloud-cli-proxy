-- 0005_claude_account_api_token.sql
-- claude_accounts 增加容器侧 API 凭据：api_token（ANTHROPIC_API_KEY 或
-- ANTHROPIC_AUTH_TOKEN，注入时按前缀区分）与 api_base_url（可选中转网关，
-- 注入为 ANTHROPIC_BASE_URL）。空串表示未配置，沿用容器内交互式登录凭据。
ALTER TABLE claude_accounts ADD COLUMN api_token TEXT NOT NULL DEFAULT '';
ALTER TABLE claude_accounts ADD COLUMN api_base_url TEXT NOT NULL DEFAULT '';
