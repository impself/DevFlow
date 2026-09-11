-- repositories：仓库接入信息

-- Webhook 按数字 ID 找仓库（owner/name 可改名，数字 ID 是唯一锚点）。
-- 查不到 = 范围外仓库：事件照记（取证），业务忽略。
-- name: GetRepositoryByNumericID :one
SELECT * FROM repositories WHERE repo_numeric_id = $1;

-- name: GetRepository :one
SELECT * FROM repositories WHERE id = $1;

-- 接入/更新仓库（能力检查通过后调用）。repo_numeric_id 是锚点：
-- 重名/改名场景按数字 ID upsert，owner/name 跟随更新。
-- name: UpsertRepository :one
INSERT INTO repositories (id, repo_numeric_id, owner, name, default_branch, installation_id, capabilities, policy_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (repo_numeric_id) DO UPDATE SET
    owner = EXCLUDED.owner,
    name = EXCLUDED.name,
    default_branch = EXCLUDED.default_branch,
    installation_id = EXCLUDED.installation_id,
    capabilities = EXCLUDED.capabilities,
    updated_at = now()
RETURNING *;
