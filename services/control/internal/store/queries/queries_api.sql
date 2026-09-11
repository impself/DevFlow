-- 对外查询与接入（T026，US3）
-- 只读为主 + 一个最小取消状态机；操作者 API 全部挂 OperatorAuth。

-- name: ListRepositories :many
SELECT * FROM repositories ORDER BY created_at DESC;

-- name: ListCases :many
SELECT c.*, r.owner AS repo_owner, r.name AS repo_name
FROM cases c JOIN repositories r ON r.id = c.repo_id
ORDER BY c.updated_at DESC
LIMIT 100;

-- name: ListRunsForCase :many
SELECT * FROM runs WHERE case_id = $1 ORDER BY created_at DESC;

-- name: ListRunCommits :many
SELECT * FROM run_commits WHERE run_id = $1 ORDER BY created_at;

-- name: ListModelCallsForRun :many
SELECT * FROM model_calls WHERE run_id = $1 ORDER BY created_at;

-- name: GetCurrentDraftForCaseId :one
SELECT r.* FROM reply_drafts r
JOIN runs ru ON r.run_id = ru.id
WHERE ru.case_id = $1 AND r.status = 'current'
ORDER BY r.created_at DESC
LIMIT 1;

-- 取消：QUEUED → CANCELLED 直接终态；RUNNING → CANCEL_REQUESTED 交执行方合作式停止。
-- name: RequestCancelRun :execrows
UPDATE runs SET status = CASE
    WHEN status = 'QUEUED' THEN 'CANCELLED'
    WHEN status = 'RUNNING' THEN 'CANCEL_REQUESTED'
    ELSE status
END, finished_at = CASE WHEN status = 'QUEUED' THEN now() ELSE finished_at END
WHERE id = $1 AND status IN ('QUEUED', 'RUNNING');
