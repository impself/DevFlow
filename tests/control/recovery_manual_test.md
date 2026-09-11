# 场景 E 恢复取证手册（T029）

> 目的：强杀 runner 后，验证租约到期接管与 lease_epoch fencing 的**取证位置**——
> PRD §11.3 / spec AC23–AC26 的人工验证步骤。前置：quickstart 的 A 场景已通
> （有完整链路），PG 与 api 正常运行。

## 步骤

1. **制造执行中的 run**：测试仓库建 Issue（触发 run），等 runner 日志出现
   `领取 run run=…`（此时 epoch=1、status=RUNNING）。

2. **强杀 runner**（模拟进程崩溃）：
   ```powershell
   # Windows：按端口/进程名强杀（不给优雅停机机会）
   taskkill /F /IM devflow-runner.exe
   ```

3. **观察租约过期（≤30s + Sweeper 周期 15s）**：
   ```sql
   -- 应看到该 run：status=RECOVERING，lease_owner 仍是死进程，epoch 不变
   SELECT id, status, lease_owner, lease_epoch, lease_expires_at, attempts
   FROM runs WHERE id = '<run_id>';
   ```
   取证点①：runner 日志（被杀前）与 api 无异常——过期由服务端时间判定，
   不依赖死者自首。

4. **重启 runner，观察接管（≤2s 轮询）**：
   - runner 日志：`领取 run run=<同 id> … epoch=2`（**epoch +1 是接管凭证**）；
   - SQL：`lease_owner=新进程 PID`、`lease_epoch=2`、`attempts+1`。
   取证点②：接管不需要人工干预，SKIP LOCKED 领取自动完成。

5. **旧 epoch 写回被拒（AC24）**：
   - 若旧进程确有迟到写回，runner 日志会出现
     `COMPLETED/FAILED 写回被拒（租约已易主）` 或执行器返回 ErrStaleEpoch；
   - 集成等价证据：`go test ./internal/controller -run TestCommitRunResult`
     的「旧纪元提交」用例（断言回执 0 条）。
   取证点③：run_commits 里**不存在**旧 epoch 的提交行
   ```sql
   SELECT commit_id, lease_epoch FROM run_commits WHERE run_id = '<run_id>';
   -- 只有 epoch=2 的行
   ```

6. **崩溃循环保护（0002 硬化）**：反复杀/接管 3 次（attempts 达 max）后：
   - SQL：`status=FAILED`、`error='重试预算耗尽…'`；
   - runner 不再领取该 run。
   集成等价证据：`go test ./internal/controller -run TestRetryBudget`。

## 结论判据（全过 = 场景 E 通过）

| 判据 | 证据位置 |
| --- | --- |
| 崩溃 run 60s 内开始恢复 | 步骤 3-4 的 SQL 时间戳 + runner 日志 |
| 接管换 epoch | runner 日志 `epoch=2` + SQL |
| 旧 epoch 无法污染结果 | run_commits 无旧行 + 集成测试 |
| 毒 run 有界终止 | 步骤 6 SQL + TestRetryBudget |
