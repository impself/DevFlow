-- cases：案例上下文

-- Webhook 同一事务里创建/更新 case：
-- 首次事件建行（title 是首次快照，之后编辑不覆盖）；后续事件只碰 updated_at。
-- name: UpsertCase :one
INSERT INTO cases (id, repo_id, issue_number, issue_id, title)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (repo_id, issue_number) DO UPDATE SET updated_at = now()
RETURNING id;

-- name: GetCase :one
SELECT * FROM cases WHERE id = $1;
