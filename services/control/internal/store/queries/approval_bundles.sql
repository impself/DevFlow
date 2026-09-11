-- approval_bundles：审批包（PRD §15，AC32–AC35）

-- 创建审批包；idempotency_key 冲突 → 无返回行，调用方取现有包回显（AC35）。
-- name: InsertApprovalBundle :one
INSERT INTO approval_bundles (id, case_id, draft_id, action_type, target, content_digest, expires_at, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetApprovalBundle :one
SELECT * FROM approval_bundles WHERE id = $1;

-- name: GetApprovalBundleByIdempotencyKey :one
SELECT * FROM approval_bundles WHERE idempotency_key = $1;

-- 批准：只允许 PENDING 且未过期且内容锚未变（AC32/AC33）。
-- 0 行 = 状态机拒绝（已批准/已过期/内容漂移），由调用方查明原因。
-- name: ApproveBundle :execrows
UPDATE approval_bundles
SET status = 'APPROVED', approved_by = $2, approved_at = now()
WHERE id = $1 AND status = 'PENDING' AND expires_at > now();

-- 拒绝：只允许 PENDING（AC35 精神：终态不可改写）。
-- name: RejectBundle :execrows
UPDATE approval_bundles
SET status = 'REJECTED'
WHERE id = $1 AND status = 'PENDING';

-- 单包失效：批准前核对发现内容/目标漂移时立即翻 EXPIRED（AC33）。
-- name: ExpireBundle :execrows
UPDATE approval_bundles SET status = 'EXPIRED'
WHERE id = $1 AND status IN ('PENDING', 'APPROVED');

-- 过期清扫：批处理把过期的 PENDING/APPROVED 翻 EXPIRED（24h 时效，PRD §15.2）。
-- name: ExpireStaleBundles :execrows
UPDATE approval_bundles
SET status = 'EXPIRED'
WHERE status IN ('PENDING', 'APPROVED') AND expires_at < now();
