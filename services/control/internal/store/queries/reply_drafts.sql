-- reply_drafts：答复草稿（审批绑定的内容锚）

-- 插入新草稿（status 默认 current）；同 case 旧草稿的 supersede 用独立语句，
-- 两者在同一事务里执行（见 SupersedeCaseDrafts）。
-- name: InsertReplyDraft :exec
INSERT INTO reply_drafts (id, run_id, artifact_id, conclusion, body_digest, evidence, needs_info_questions)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- 同 case 的现有 current 草稿全部翻 superseded：草稿版本「每 case 一个 current」。
-- 跨 run 关联需要经过 runs 表（draft 没有 case 列）。
-- name: SupersedeCaseDrafts :execrows
UPDATE reply_drafts SET status = 'superseded'
WHERE status = 'current'
  AND id IN (
    SELECT r.id FROM reply_drafts r
    JOIN runs ru ON r.run_id = ru.id
    WHERE ru.case_id = $1
  );

-- name: GetCurrentDraftForCase :one
SELECT r.* FROM reply_drafts r
JOIN runs ru ON r.run_id = ru.id
WHERE ru.case_id = $1 AND r.status = 'current'
ORDER BY r.created_at DESC
LIMIT 1;
