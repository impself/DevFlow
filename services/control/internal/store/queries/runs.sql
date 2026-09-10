-- runs：租约协议的全部 SQL（protocol 见 data-model.md，AC23–AC26）

-- 领取：单条语句内完成「选行 → 加锁 → 改状态」= 原子领取。
-- FOR UPDATE SKIP LOCKED：多个 worker 并发领取时，已被别人锁住的行直接跳过，
-- 各拿各的，永不互等（对比：先 SELECT 再 UPDATE 的两步写法在并发下会重复领取）。
-- lease_epoch +1：每次交接都换纪元，旧纪元的写回一律拒绝（AC24）。
-- 无可领取行时子查询返回 NULL → UPDATE 0 行 → :one 得到 ErrNoRows，worker 据此休眠。
-- name: ClaimRun :one
UPDATE runs SET
    status = 'RUNNING',
    lease_owner = $1,
    lease_epoch = lease_epoch + 1,
    lease_expires_at = now() + interval '30 seconds',
    started_at = COALESCE(started_at, now())
WHERE id = (
    SELECT id FROM runs
    WHERE status IN ('QUEUED', 'RECOVERING')
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, case_id, trigger_event_id, input_snapshot, lease_epoch;

-- 心跳续租：WHERE 带上 owner+epoch 双重身份。
-- 影响行数为 0 = 租约已被服务器易主，持有者必须立刻停止新增工作（PRD §11.3）。
-- name: HeartbeatRun :execrows
UPDATE runs SET lease_expires_at = now() + interval '30 seconds'
WHERE id = $1 AND lease_owner = $2 AND lease_epoch = $3;

-- 原子提交回执：同 (run_id, commit_id) 重放 → DO NOTHING（幂等回执，AC25）。
-- 冲突时无返回行，controller 再用 GetRunCommitPayloadHash 比对：
-- 同 hash = 重放，回执已存在；不同 hash = 同 ID 不同内容，409 冲突（AC26）。
-- name: InsertRunCommit :one
INSERT INTO run_commits (run_id, commit_id, payload_hash, lease_epoch, stage, result, seq)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_id, commit_id) DO NOTHING
RETURNING payload_hash;

-- name: GetRunCommitPayloadHash :one
SELECT payload_hash FROM run_commits
WHERE run_id = $1 AND commit_id = $2;

-- 终态落库：WHERE 双重身份校验，0 行 = 旧 epoch 僵尸写回被拒（AC24）。
-- name: CompleteRun :execrows
UPDATE runs SET status = 'COMPLETED', outcome = $2, finished_at = now()
WHERE id = $1 AND lease_owner = $3 AND lease_epoch = $4;

-- name: FailRun :execrows
UPDATE runs SET status = 'FAILED', error = $2, finished_at = now()
WHERE id = $1 AND lease_owner = $3 AND lease_epoch = $4;

-- 清道夫：租约过期的 RUNNING 统统翻成 RECOVERING，等待被重新领取接管。
-- 由 runner 进程周期调用（与领取循环同进程，无需心跳方自己认罪）。
-- name: ExpireStaleRuns :execrows
UPDATE runs SET status = 'RECOVERING'
WHERE status = 'RUNNING' AND lease_expires_at < now();

-- name: GetRun :one
SELECT * FROM runs WHERE id = $1;

-- 取消检查：执行方在长步骤之间轮询，尽早发现 CANCEL_REQUESTED 并停手。
-- name: GetRunStatus :one
SELECT status FROM runs WHERE id = $1;

-- Webhook 同一事务里创建 run：状态默认 QUEUED，等 worker 领取。
-- input_snapshot 是触发时刻的固定快照（issue 内容版本 + 策略版本，FR-3），
-- 之后无论 Issue 怎么编辑，本次执行都以快照为准。
-- name: InsertRun :one
INSERT INTO runs (id, case_id, trigger_event_id, input_snapshot)
VALUES ($1, $2, $3, $4)
RETURNING id, status;
