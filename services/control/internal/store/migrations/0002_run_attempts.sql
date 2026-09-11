-- 0002：run 重试预算与领取确定性（分布式协议调研改进项 #5/#6）
-- 背景：epoch 只防双主不防毒任务——反复崩溃的 run 会无限 RECOVERING 循环。
-- 对齐 pg-boss retry_limit / Graphile attempts 语义：领取时 attempts+1，
-- 达到 max_attempts 的 run 不可再领取，由清道夫判 FAILED 终结。
ALTER TABLE runs ADD COLUMN attempts integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN max_attempts integer NOT NULL DEFAULT 3;
