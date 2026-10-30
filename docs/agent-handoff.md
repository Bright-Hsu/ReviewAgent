# ReviewAgent 开发交接说明

> 面向下一位开发 Agent。最后验证：`go test ./...` 通过。本文描述的是当前代码实际状态，而不是完整规划的目标状态。

## 1. 项目目标

ReviewAgent 是一个 Go 实现的 GitHub Pull Request AI Code Review Agent。它的设计原则是：

- 确定性程序处理 Webhook 验签、Diff 解析、文件筛选、行号校验、幂等、状态和 GitHub 评论发布；
- LLM 只负责代码缺陷判断与受控上下文探索；
- 只有有证据、置信度合格且能落到 PR 新增行的 Finding 才能发布为行内评论；
- Agent 不可执行 Shell、写文件或访问任意网络地址。

更完整的功能路线在 [code-review-agent-feature-plan.md](code-review-agent-feature-plan.md)。

## 2. 当前技术栈与启动方式

- Go 1.23
- 仅标准库 HTTP + `modernc.org/sqlite`（纯 Go SQLite 驱动）
- GitHub REST API
- OpenAI Chat Completions 兼容接口

常用命令：

```powershell
go test ./...
go vet ./...
go run ./cmd/reviewagent
go run ./cmd/reviewagent -fixture ./fixtures/error_ignored.diff
```

环境变量模板见项目根目录的 `.env.example`：

- `PORT`：默认 `8080`
- `WEBHOOK_SECRET`：GitHub Webhook HMAC-SHA256 密钥，生产必填
- `GITHUB_TOKEN`：当前实现使用的 GitHub REST API Token；需要 Pull requests 读写与 Contents 读取权限
- `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`：OpenAI 兼容模型配置
- `DATABASE_URL`：默认 SQLite 文件，启用 WAL 和 busy timeout
- `ADMIN_TOKEN`：保护 `GET /runs/{id}` 状态查询接口；生产必填
- `MIN_CONFIDENCE`、`MAX_FILES`、`MAX_PATCH_LINES`：审查预算

当前默认数据库文件为 `reviewagent.db`，且被 `.gitignore` 忽略。它是运行时数据，不是源码的一部分。

## 3. 当前端到端链路

```text
GitHub pull_request Webhook
  -> HMAC 验签、事件筛选（opened / reopened / synchronize）
  -> SQLite 原子领取 ReviewRun（按 owner/repo/PR/head SHA 去重）
  -> queued -> running
  -> 获取 PR 文件与 patch
  -> 文件筛选与规模限制
  -> 逐文件 LLM Agent Review
       -> 可选工具调用循环
       -> 返回 Finding JSON
  -> Finding 确定性校验、单文件去重
  -> GitHub 行内评论 + PR 汇总评论
  -> completed / partial / failed
```

代码入口：

| 文件 | 职责 |
| --- | --- |
| `cmd/reviewagent/main.go` | 服务启动、fixture 回放、优雅关闭。 |
| `internal/app/service.go` | Webhook、审查编排、状态流转、查询接口。 |
| `internal/github/client.go` | GitHub PR 文件、评论、汇总评论 REST 调用。 |
| `internal/diff/parse.go` | Unified Diff 新增行解析。 |
| `internal/review/policy.go` | 文件选择与 Finding 的确定性校验。 |
| `internal/llm/openai.go` | OpenAI 兼容 Agent tool loop。 |
| `internal/github/context_tools.go` | commit 固定的受控仓库上下文工具。 |
| `internal/store/sqlite.go` | SQLite 状态机与幂等领取。 |

## 4. 已实现功能

### 4.1 GitHub 与审查流水线

- Webhook 请求体大小限制为 1 MiB，并校验 `X-Hub-Signature-256`。
- 只接受 PR 的 `opened`、`reopened`、`synchronize` 事件。
- 通过 GitHub API 获取 PR 文件、patch、状态和新增量。
- 跳过 `vendor/`、`node_modules/`、`dist/`、`coverage/`、锁文件、压缩 JS、缺失 patch 和超大 patch。
- 限制最多审查文件数与单文件 Diff 行数。
- 使用 GitHub PR inline comment API 发布 Finding，使用 issue comment API 发布摘要。
- 当前认证仍是 `GITHUB_TOKEN`；尚未实现 GitHub App Installation Token。

### 4.2 Finding 安全边界

模型必须返回 JSON：`title`、`body`、`severity`、`confidence`、`file`、`new_line`、`evidence`，可选 `suggestion`。

发布前 `internal/review/policy.go` 会验证：

- severity 合法；
- 置信度不低于 `MIN_CONFIDENCE`；
- Finding 的文件等于正在审查的文件；
- title、body、evidence 完整；
- `new_line` 确实是当前 Diff 的新增行。

因此模型不能把评论落在删除行或任意仓库位置。当前去重范围是同一文件内的 `file + line + title`。

### 4.3 Agent tool loop

`internal/llm/openai.go` 实现了：

```text
发送 Diff 和审查约束
  -> 模型返回 tool_calls 或最终 Finding JSON
  -> 服务执行每一个受控工具调用
  -> 将 tool 结果以 tool message 回传
  -> 重复，直到没有 tool_calls 或到达预算
```

- 每次文件审查最多 `4` 次工具调用（当前常量 `maxToolCalls`）。
- 单文件审查在服务层有 `60 秒` context timeout。
- 模型最终不再请求工具时，服务将其 content 解码为 Finding JSON。
- 工具错误会作为普通 tool result 回传模型，使模型可调整策略。

当前可用工具：

| 工具 | 行为与约束 |
| --- | --- |
| `read_file` | 读取 PR head SHA 的仓库文件范围；最多 160 行 / 24 KiB。 |
| `read_diff` | 读取本 PR 某文件的 patch，或受 48 KiB 限制的全部 patch。 |
| `list_files` | 列出 PR head SHA 的最多 100 个文件。 |
| `search_code` | 使用固定 commit 的 Git tree + blob API，不使用浮动版本的全局搜索；最多扫描 30 个源码文件、120 KiB，返回最多 12 条结果。 |

路径必须是仓库内、正斜杠相对路径。绝对路径、反斜杠和路径逃逸被拒绝。工具层不暴露 Shell、写入操作或任意 URL 请求。

注意：`search_code` 为成本和安全设置了严格上限，不能保证扫描大型仓库的所有文件；这是一种有意的降级行为。

### 4.4 持久化状态机

SQLite 表 `review_runs` 以 `(owner, repo, pr_number, head_sha)` 唯一约束提供跨重启幂等性。

状态：

```text
queued -> running -> completed
                  -> partial
                  -> failed
```

- 领取重复 commit 时直接跳过，不会重新发表评论。
- 获取 Diff、LLM 审查、发布汇总失败时标记 `failed` 并存储错误原因。
- 个别行内评论发布失败、但汇总成功时标记 `partial`。
- `GET /runs/{id}` 返回运行记录；需 `Authorization: Bearer <ADMIN_TOKEN>`。没有配置或 token 不正确时返回 404，避免暴露运行信息。

## 5. 测试现状

已覆盖并通过：

- Unified Diff 新增行识别；
- Finding 不能落到非新增行；
- Webhook HMAC 验签；
- Agent 一次工具调用后返回 Finding 的循环；
- 工具路径安全；
- commit 固定的 `read_diff`、`list_files`、`search_code`；
- SQLite 生命周期、持久化幂等与非法状态转换；
- 运行查询接口的 `ADMIN_TOKEN` 保护。

每次改动后至少运行：

```powershell
gofmt -w cmd internal
go test ./...
go vet ./...
```

尚未用真实 GitHub App、真实 LLM API 或公共仓库执行端到端发布测试。不要宣称这些外部集成已经在生产环境验证。

## 6. 已知限制与高优先级后续工作

建议下一位 Agent 按下面顺序继续。

### P0：可靠执行与鉴权

1. **任务队列与恢复**：Webhook 目前 goroutine 内直接执行；进程崩溃后 `queued/running` 任务不会自动恢复。实现 worker、租约/心跳、有限重试、指数退避与服务启动时恢复。
2. **GitHub App Authentication**：替换静态 `GITHUB_TOKEN`，实现 App JWT 和 Installation Token，缓存并在到期前刷新。
3. **旧 commit 取消**：同一 PR 出现新 head SHA 时，取消或丢弃旧 SHA 尚未开始的任务。

### P1：Review 质量与 Agent loop

4. **跨文件语义分组和并发调度**：目前按文件串行审查；应按路径/符号/依赖关系组成 ChangeGroup，再受限并发执行。
5. **显式 `finish` 工具**：当前“无 tool call + JSON content”表示结束。可增加 `finish(findings)`，用 schema 校验的工具调用作为唯一终点，降低 provider 输出不一致风险。
6. **调用轨迹与预算**：持久化每个 tool call 的名称、参数摘要、耗时、返回大小、错误；记录模型 Token、费用、最大上下文与总耗时。
7. **跨文件去重和评论验证**：当前只做单文件字符串去重；需要全局语义去重、证据一致性检查和可选的二次验证 Agent。
8. **评论重定位**：PR 更新后，基于代码片段和邻近上下文重新找到行；失败时改为 summary，不强发 inline comment。

### P2：工程信号与产品化

9. `.reviewagent.yml`：路径、语言、阈值、预算、业务规则与严重度覆盖。
10. Go 静态分析（`go vet`、`staticcheck`）与安全隔离的测试执行。
11. 评估 fixture：标注真实/注入缺陷，统计 precision、recall、错误定位率、延迟和成本。
12. 运行查看页面或 CLI、人工“有用/无用”反馈。

## 7. 接手时的实现约束

- 保持“确定性基础设施 + 受控 Agent 判断”的边界；不要让 LLM 直接执行 GitHub 写入、Shell 或网络请求。
- 发布评论前必须保留新增行验证；这是防止无效/过时 inline comment 的核心约束。
- 新增外部输入、数据库迁移、路径处理和网络调用时补充单元测试。
- `GITHUB_TOKEN`、`LLM_API_KEY`、`WEBHOOK_SECRET`、`ADMIN_TOKEN` 均不得写入仓库或日志。
- SQLite 适合当前单实例 MVP；若扩展到多副本 worker，应迁移到 PostgreSQL 或其他支持并发 worker 的数据库。
- 当前仓库没有 Git 初始化信息；提交、分支和远端配置需由项目拥有者另行建立。

## 8. 给下一位 Agent 的推荐起始提示词

```text
阅读 docs/agent-handoff.md、docs/code-review-agent-feature-plan.md、README.md 和当前代码。
ReviewAgent 是 Go 项目，当前已有 GitHub PR Diff 审查、受控多工具 LLM loop、SQLite 状态机。
继续实现 P0 的可靠任务队列与崩溃恢复：不要扩大 Agent 权限，不要破坏 Finding 的新增行校验。
先提出最小数据模型/状态迁移方案，再修改代码；为恢复、重试、重复 Webhook 和新 head SHA 场景补充测试；最后运行 gofmt、go test ./... 和 go vet ./...。
```
