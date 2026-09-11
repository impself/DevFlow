-- actions：外部动作实例（US2 发布链）

-- bundle:action 恒 1:1 由 UNIQUE(actions.bundle_id) 库级保证（review P2-3）：
-- 并发插入第二个 action 被 ON CONFLICT 吞掉（无返回行），调用方回读现有行。
-- name: InsertAction :one
INSERT INTO actions (id, bundle_id, type)
VALUES ($1, $2, 'POST_ISSUE_COMMENT')
ON CONFLICT (bundle_id) DO NOTHING
RETURNING *;

-- name: GetActionByBundle :one
SELECT * FROM actions WHERE bundle_id = $1;

-- PENDING → EXECUTING：0 行 = 已被他处推进，调用方取当前状态回显。
-- name: MarkActionExecuting :execrows
UPDATE actions SET status = 'EXECUTING'
WHERE id = $1 AND status = 'PENDING';

-- 执行成功：落回执锚点（remote_comment_id 是后续核对的身份锚，AC36）。
-- EXECUTING/RECONCILING 都可以到 SUCCEEDED（后者来自核对命中）。
-- name: MarkActionSucceeded :execrows
UPDATE actions
SET status = 'SUCCEEDED', remote_comment_id = $2, remote_url = $3,
    receipt = $4, executed_at = now(), reconciled_at = now()
WHERE id = $1 AND status IN ('EXECUTING', 'RECONCILING');

-- 明确失败（GitHub 返回 4xx 类确定错误）：绝不自动重发。
-- name: MarkActionFailed :execrows
UPDATE actions
SET status = 'FAILED', receipt = $2, executed_at = now()
WHERE id = $1 AND status = 'EXECUTING';

-- 响应丢失/网络不确定：进入核对态（AC36），等 reconcile 裁决。
-- name: MarkActionReconciling :execrows
UPDATE actions SET status = 'RECONCILING'
WHERE id = $1 AND status = 'EXECUTING';

-- 回退为 PENDING：发布链在「从未触碰 GitHub」的步骤失败（如 preflight 的
-- 基础设施错误）时使用——恢复分发据此区分「未执行，可继续」与
-- 「执行过但结果未知，须核对（AC36）」。
-- name: ResetActionToPending :execrows
UPDATE actions SET status = 'PENDING'
WHERE id = $1 AND status = 'EXECUTING';

-- bundle 状态跟随 action 推进（APPROVED → EXECUTING → EXECUTED）。
-- name: UpdateBundleStatus :execrows
UPDATE approval_bundles SET status = $2
WHERE id = $1 AND status = $3;

-- name: GetAction :one
SELECT * FROM actions WHERE id = $1;
