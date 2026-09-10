-- repositories：仓库接入信息

-- Webhook 按数字 ID 找仓库（owner/name 可改名，数字 ID 是唯一锚点）。
-- 查不到 = 范围外仓库：事件照记（取证），业务忽略。
-- name: GetRepositoryByNumericID :one
SELECT * FROM repositories WHERE repo_numeric_id = $1;

-- name: GetRepository :one
SELECT * FROM repositories WHERE id = $1;
