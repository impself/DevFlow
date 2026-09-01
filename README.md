# DevFlow

围绕 GitHub Issue 与代码证据工作的研发协作 Agent：判断问题需要解答、补充信息还是
代码修复，自动形成解释、补丁与验证结果，再由维护者**人工审批**后对外发布。

> 当前状态：**设计与规格阶段**。PRD（V3.0）已完成需求访谈定稿，代码尚未实现；
> 文中一切能力、数字与验收标准均为目标，不代表已有成果。

## 文档结构

| 位置 | 内容 |
| --- | --- |
| `docs/DevFlow 完整 PRD.md` | **最高需求源**：V3.0 完整规格，含 FR-01~FR-20、AC01–AC50 验收清单、M1–M6 路线图 |
| `docs/DevFlow 核心技术架构 PRD.md` | 架构摘要（V0.3，以完整 PRD 为准） |
| `docs/AliGo 架构学习与 DevFlow 映射.md` | 参考架构（阿里 AliGo）与本项目职责映射，区分参考事实与自行设计 |
| `.specify/memory/constitution.md` | **项目宪法**：不可妥协原则（安全红线、人工审批、隔离、合同优先等），优先级高于一切 spec 与实现 |
| `specs/` | 按纵向切片落地的功能规格（Spec Kit 管理） |

## Spec 驱动开发（GitHub Spec Kit）

本仓库使用 [GitHub Spec Kit](https://github.com/github/spec-kit) v1.0.1 做 spec 驱动开发，
Agent 集成为 **ZCode（skills 模式，`.zcode/skills/speckit-*`）**。

工作流：

```
$speckit-constitution   项目原则（已完成，v1.0.0）
→ $speckit-specify      功能规格（WHAT/WHY，不含技术方案）
→ $speckit-clarify      （可选）结构化质询，消除规格歧义
→ $speckit-plan         技术方案与研究（HOW）
→ $speckit-tasks        任务拆解
→ $speckit-analyze      （可选）跨产物一致性检查
→ $speckit-implement    按任务执行实现
```

约定：

1. **宪法 > PRD > spec**：冲突时按宪法 Governance 流程处理；每个 spec 必须标注
   与 PRD 的 FR/AC 追踪关系。
2. **纵向切片**：每个 spec 对应一个真实案例打通的端到端切片，对齐 PRD §27.3；
   不预先一次性建设全部数据结构。
3. **完成定义**：相关 AC 拿到执行证据（数据库状态、Artifact、受控测试对象或日志），
   而不是代码写完。
4. 文档正文用中文；代码标识符、路径、Schema 字段保留英文。

## 里程碑（PRD §27.2）

| 阶段 | 时间 | 内容 |
| --- | --- | --- |
| M1 可信接入与回答 | 2026-09 | Fixture 基线、GitHub App、Webhook、Run 最小状态与租约、Issue Agent、引用答复、最小 UI、批准后发评论 |
| M2 代码修复闭环 | 2026-10 | Runner 隔离、Repo/Repair Agent、最小动态 DAG、补丁与测试 |
| M3 编排与恢复 | 2026-11 | Review、重规划、原子提交回执、取消/接管、预算 |
| M4 批准发布与 CI 回流 | 2026-12 | 完整审批、Publisher、分支与 Draft PR、CI Agent、测试仓库限时自动发布 |
| M5 评测与贡献试点 | 2027-01 | P0 验收、三策略对照评测、安全检查 |
| M6 呈现与稳定性 | 2027-02 | 演示、安装手册、简历与面试材料 |

## 当前 Spec 状态

| Spec | 名称 | 状态 | 里程碑 |
| --- | --- | --- | --- |
| [001-m1-vertical-slice](specs/001-m1-vertical-slice/spec.md) | M1 主线竖切：Issue 接收到有据答复与批准发布 | Draft（待审核） | M1 |

## 开发环境现状（2026-09-01 核对）

- 就绪：git 2.55、Node 22、Docker 29、PostgreSQL 18.1、specify 1.0.1
- Go 1.24.0（`C:\Program Files\Go\bin`）：已安装；部分 shell 的 PATH 未包含它，
  命令找不到时把该目录加入 PATH 或使用完整路径
- Python：conda base 为 3.13.5（`D:\conda\python.exe`，满足 AgentScope ≥3.10）；
  另有 Python 3.9（`D:\python39`）排在 PATH 前列，注意不要误用。
  实现 runtime 服务时为项目创建独立虚拟环境（conda env 或 uv venv）
