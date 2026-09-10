-- 0001_init.sql — M1 数据模型 V1（依据 specs/001-m1-vertical-slice/data-model.md）
-- 约定：时间一律 timestamptz（UTC 存储）；主键为无业务含义字符串 ID，由应用侧生成；
-- 大内容（草稿正文、引用原文）存 Artifact 目录，表内只存引用与哈希。
-- 状态机一律用 CHECK 约束兜底：数据库是业务状态唯一权威来源（宪法 IV），
-- 不可能状态（如 COMPLETED 后再变 QUEUED）在存储层就该被拒绝。

-- ===== 操作者 =====
CREATE TABLE operators (
    id             text PRIMARY KEY,
    github_user_id bigint NOT NULL UNIQUE,
    display_name   text,
    is_admin       boolean NOT NULL DEFAULT true,  -- M1 恒 true（单操作者）
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- ===== 已接入仓库 =====
-- repo_numeric_id 为准（owner/name 可改名）；capabilities 是实测能力快照
CREATE TABLE repositories (
    id               text PRIMARY KEY,
    repo_numeric_id  bigint NOT NULL UNIQUE,
    owner            text NOT NULL,
    name             text NOT NULL,
    default_branch   text NOT NULL,
    installation_id  bigint NOT NULL,
    capabilities     jsonb NOT NULL DEFAULT '{}',
    policy_version   text NOT NULL,
    status           text NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active', 'disabled')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner, name)
);

-- ===== 原始事件（投递去重锚点） =====
-- delivery_id 唯一 = GitHub webhook 幂等的第一道闸（AC45）。
-- run_id 的外键在 runs 表创建后用 ALTER 补上（两者互相引用）。
CREATE TABLE inbox_events (
    id              text PRIMARY KEY,
    delivery_id     text NOT NULL UNIQUE,
    event_type      text NOT NULL,
    action          text,
    repo_numeric_id bigint,
    payload         jsonb NOT NULL,
    signature_valid boolean NOT NULL,
    process_status  text NOT NULL DEFAULT 'received'
                    CHECK (process_status IN
                        ('received', 'deduplicated', 'ignored', 'processed', 'failed')),
    run_id          text,
    received_at     timestamptz NOT NULL DEFAULT now(),
    processed_at    timestamptz
);
CREATE INDEX inbox_events_received_at_idx ON inbox_events (received_at);
CREATE INDEX inbox_events_repo_idx ON inbox_events (repo_numeric_id);

-- ===== 案例上下文 =====
-- display_state 是派生态（查询便捷），权威状态在 runs/reply_drafts/approval_bundles
CREATE TABLE cases (
    id            text PRIMARY KEY,
    repo_id       text NOT NULL REFERENCES repositories(id),
    issue_number  bigint NOT NULL,
    issue_id      bigint NOT NULL UNIQUE,  -- GitHub 全局 databaseId，天然跨仓唯一
    title         text NOT NULL,           -- 首次快照标题，不随后续编辑变化
    display_state text NOT NULL DEFAULT 'analyzing'
                  CHECK (display_state IN
                      ('analyzing', 'waiting_info', 'awaiting_approval', 'replied', 'closed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, issue_number)
);

-- ===== 一次有预算的执行 =====
-- 领取/心跳/提交协议见 queries（T007）；部分索引只覆盖可领取状态，
-- 让「找活干」的查询在百万行 runs 表上仍走索引、且索引体积与存量无关。
-- 注：data-model.md 写的是 (status) 部分索引；这里改为 (created_at)——
--     领取 SQL 是 WHERE status IN(...) ORDER BY created_at LIMIT 1，
--     部分索引天然过滤了状态，(created_at) 序才能配合 ORDER BY 走索引扫描。
CREATE TABLE runs (
    id               text PRIMARY KEY,
    case_id          text NOT NULL REFERENCES cases(id),
    trigger_event_id text NOT NULL REFERENCES inbox_events(id),
    input_snapshot   jsonb NOT NULL,         -- 固定快照：issue 版本、head SHA、策略版本（FR-3）
    status           text NOT NULL DEFAULT 'QUEUED'
                     CHECK (status IN
                         ('QUEUED', 'RUNNING', 'RECOVERING', 'CANCEL_REQUESTED',
                          'CANCELLED', 'COMPLETED', 'FAILED')),
    outcome          text
                     CHECK (outcome IN
                         ('ANSWER_READY', 'NEEDS_INFO', 'UNRESOLVED',
                          'LIMIT_REACHED', 'STALE_INPUT')),
    lease_owner      text,
    lease_epoch      bigint NOT NULL DEFAULT 0,  -- 每次领取 +1（AC24 防僵尸写回）
    lease_expires_at timestamptz,
    model_calls_used integer NOT NULL DEFAULT 0,
    max_model_calls  integer NOT NULL DEFAULT 24,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz
);
CREATE INDEX runs_claimable_idx ON runs (created_at)
    WHERE status IN ('QUEUED', 'RECOVERING');
-- 清道夫索引：ExpireStaleRuns 扫「RUNNING 且租约已过期」，没有它 runs 上量后变全表扫
CREATE INDEX runs_running_lease_idx ON runs (lease_expires_at)
    WHERE status = 'RUNNING';
CREATE INDEX runs_case_idx ON runs (case_id);

-- ===== 原子提交回执（协议表，AC23–AC26） =====
-- (run_id, commit_id) 联合主键：同 commit_id 重放=幂等回执；
-- payload_hash 不同=409 冲突（AC26）；lease_epoch 校验挡旧执行者写回（AC24）。
CREATE TABLE run_commits (
    run_id      text NOT NULL REFERENCES runs(id),
    commit_id   text NOT NULL,
    payload_hash text NOT NULL,
    lease_epoch bigint NOT NULL,
    stage       text NOT NULL DEFAULT 'issue-analysis',
    result      jsonb NOT NULL,              -- run-commit.schema.json 结构
    seq         bigint NOT NULL,             -- Run 内递增事件序号（PRD §11.4）
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, commit_id)
);

-- ===== 不可变产物（content-addressable） =====
-- content_digest 唯一 = 同内容只存一份文件；表里只有引用，正文在 Artifact 目录
CREATE TABLE artifacts (
    id             text PRIMARY KEY,
    run_id         text NOT NULL REFERENCES runs(id),
    kind           text NOT NULL
                   CHECK (kind IN ('reply_draft', 'evidence_pack', 'raw_model_io')),
    content_digest text NOT NULL UNIQUE,     -- sha256，即文件名
    size_bytes     integer NOT NULL,
    schema_version text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- ===== 答复草稿 =====
-- body_digest 是审批绑定的内容锚（AC33）；status 表达「同 Case 多轮草稿」的版本关系
CREATE TABLE reply_drafts (
    id                  text PRIMARY KEY,
    run_id              text NOT NULL REFERENCES runs(id),
    artifact_id         text NOT NULL REFERENCES artifacts(id),
    conclusion          text NOT NULL
                        CHECK (conclusion IN ('ANSWER_READY', 'NEEDS_INFO')),
    body_digest         text NOT NULL,
    evidence            jsonb NOT NULL DEFAULT '[]',  -- {source_type,path,line_start,line_end,sha,quote_digest}
    needs_info_questions jsonb,
    status              text NOT NULL DEFAULT 'current'
                        CHECK (status IN ('current', 'superseded')),
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reply_drafts_run_idx ON reply_drafts (run_id);

-- ===== 审批包（PRD §15） =====
-- 绑定三元组：target（精确到 numeric_id+issue_number）+ content_digest + 24h 时效。
-- 任一变化即失效（AC33）；idempotency_key 让重复批准回显原结果（AC35）。
CREATE TABLE approval_bundles (
    id              text PRIMARY KEY,
    case_id         text NOT NULL REFERENCES cases(id),
    draft_id        text NOT NULL REFERENCES reply_drafts(id),
    action_type     text NOT NULL DEFAULT 'POST_ISSUE_COMMENT'
                    CHECK (action_type IN ('POST_ISSUE_COMMENT')),  -- M1 唯一对外动作
    target          jsonb NOT NULL,          -- {repo_numeric_id, issue_number}
    content_digest  text NOT NULL,
    status          text NOT NULL DEFAULT 'PENDING'
                    CHECK (status IN
                        ('PENDING', 'APPROVED', 'EXECUTING', 'EXECUTED',
                         'REJECTED', 'EXPIRED')),
    expires_at      timestamptz NOT NULL,    -- created_at + 24h
    approved_by     text,
    approved_at     timestamptz,
    idempotency_key text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_bundles_case_idx ON approval_bundles (case_id);

-- ===== 外部动作实例 =====
-- 状态机含 RECONCILING（响应丢失核对，AC36）：不盲目重发，核对优先
CREATE TABLE actions (
    id                text PRIMARY KEY,
    bundle_id         text NOT NULL REFERENCES approval_bundles(id),
    type              text NOT NULL DEFAULT 'POST_ISSUE_COMMENT'
                      CHECK (type IN ('POST_ISSUE_COMMENT')),
    status            text NOT NULL DEFAULT 'PENDING'
                      CHECK (status IN
                          ('PENDING', 'EXECUTING', 'RECONCILING',
                           'SUCCEEDED', 'FAILED', 'UNCERTAIN')),
    remote_comment_id bigint,               -- 核对锚点
    remote_url        text,
    receipt           jsonb,                -- action-receipt.schema.json 结构
    attempts          integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    executed_at       timestamptz,
    reconciled_at     timestamptz
);
CREATE INDEX actions_bundle_idx ON actions (bundle_id);

-- ===== 模型调用与费用（FR-10，AC47） =====
-- cost 不得记零：估算不出也要给非零估计 + estimated 标记
CREATE TABLE model_calls (
    id            text PRIMARY KEY,
    run_id        text NOT NULL REFERENCES runs(id),
    provider      text NOT NULL DEFAULT 'dashscope',
    model         text NOT NULL,
    input_tokens  bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cost_cny      numeric(12, 4) NOT NULL CHECK (cost_cny > 0),
    cost_status   text NOT NULL CHECK (cost_status IN ('estimated', 'confirmed')),
    trace_id      text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_calls_run_idx ON model_calls (run_id);

-- 补齐 inbox_events ↔ runs 的互相引用（先建表后加外键，破循环）
ALTER TABLE inbox_events
    ADD CONSTRAINT inbox_events_run_fk FOREIGN KEY (run_id) REFERENCES runs(id);
