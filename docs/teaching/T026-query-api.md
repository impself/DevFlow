# T026 教学笔记：查询与接入 API——为工作台组装的读模型

> 对应文件：`internal/controller/query.go`、`queries/queries_api.sql`、
> `internal/github/resolver.go`（CapabilityProber）、路由接线
> 面试视角：**读写分离的读模型** + 能力检查的「实测而非声明」原则。后端干货，
> 前端页面（T027/T028）只是这个 API 的消费方。

## 1. 读模型：API 形状跟着消费者走，不跟着表走

`GetCase` 一次返回 case + runs 轨迹 + current 草稿 + 费用汇总——四张表的数据
组装成一个响应。**工作台详情页要的就是这一屏**，前端不用拼四次请求。
这是读模型（read model）思路：写侧按规范化表存（一致性），读侧按消费场景
组装（便利性）。M1 规模直接 join/多查询组装即可，不需要物化视图——
**过早优化读模型是分布式面试里的常见陷阱，能说清"什么时候才需要"比会用
CQRS 术语加分**。

费用汇总是个细节：`cost_cny_total` 用 `Float64Value()` 从 pgtype.Numeric 转出
再累加——展示层的 float 舍入可接受（写入侧仍是 Numeric，精度问题只存在于
展示，不存在于账本）。

## 2. 能力检查：实测而非声明（AC01）

接入仓库时不信 App 权限页的「声明」，**实际调一次 API**：
- `GetRepo` 成功 = metadata_read + contents_read 旁证 + 拿到默认分支；
- `ListIssues(1)` 成功 = issues_read 旁证；
- **issues_write 特殊**：无法无损实测（发一条再删会留痕）——以 App 权限配置
  为准，真正的验证发生在 Checkpoint 场景 B 的真实发布。代码注释写明了这个
  取舍：**不能验证的能力就明说不能验证**，假装验证比不验证更糟。

缺失时 422 + `missing` 清单 + `required` 全集（AC01 的字面落地），仓库**不落
active**（spec FR-1）——错误路径也是数据：操作者看到的是"缺什么"，不是
一句 failed。

## 3. 取消：最小状态机，一条 SQL

```sql
UPDATE runs SET status = CASE
    WHEN status = 'QUEUED'    THEN 'CANCELLED'       -- 没人领，直接终态
    WHEN status = 'RUNNING'   THEN 'CANCEL_REQUESTED' -- 执行中，合作式停止
END WHERE id = $1 AND status IN ('QUEUED', 'RUNNING');
```

一条 SQL 完成读-判-写（无竞态窗口）：QUEUED 直接取消（领取 SQL 的 WHERE
不含 CANCELLED，永不冲突）；RUNNING 转 CANCEL_REQUESTED，执行方在下一个
检查点（`CancelRequested` 钩子，T013）自愿停手。终态 run 取消 → 409 回显
当前状态（幂等语义，不是报错）。

## 4. Go 细节：`json.RawMessage` 透传 jsonb

db 层的 `[]byte`（jsonb）直接 `json.RawMessage` 塞进 gin.H——**不反序列化再
序列化**：省一次 alloc，且字段原样透传（前端拿到的 evidence 结构与库里的
完全一致）。嵌套 JSON 在响应里当透明管道用，是 Go 处理 jsonb 的惯用法。

## 5. 自测证据

5 个用例：列表+详情（草稿/费用齐）、run 详情（提交轨迹/逐调用费用）、
终态取消 409、能力缺失 422 带清单、能力齐全 200 落 active。全套回归绿。
