-- inbox_events：webhook 入口的原件登记与投递去重（AC45）
-- ON CONFLICT DO NOTHING：重复投递静默放弃；:one 无返回行（ErrNoRows）即重复，
-- 由 controller 解释为「去重返回 200」，不让数据库错误语义泄漏到 HTTP 层。

-- name: InsertInboxEvent :one
INSERT INTO inbox_events (id, delivery_id, event_type, action, repo_numeric_id, payload, signature_valid)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (delivery_id) DO NOTHING
RETURNING id, received_at;
