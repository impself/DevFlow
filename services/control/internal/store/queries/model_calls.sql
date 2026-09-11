-- model_calls：模型调用与费用（FR-10，AC47）
-- 费用记录失败只记日志不失败执行：记账是观测需求，不该比业务更致命。
-- name: InsertModelCall :exec
INSERT INTO model_calls (id, run_id, model, input_tokens, output_tokens, cost_cny, cost_status, trace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- 预算护栏：每次模型调用后递增；执行方在下一次调用前检查 used < max。
-- 不带 epoch 校验：僵尸的调用也真实花了钱，预算必须如实累计。
-- name: IncrementModelCallsUsed :execrows
UPDATE runs SET model_calls_used = model_calls_used + 1 WHERE id = $1;
