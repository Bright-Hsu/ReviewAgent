# ReviewAgent 功能规划（v0.1）

> 项目定位：一个面向 GitHub Pull Request 的 AI Code Review Agent。它把确定性的代码变更处理、风险控制和评论定位，与 LLM 的缺陷判断和上下文探索结合起来，自动输出可追溯、可定位的审查建议。

## 1. 目标与边界

### 1.1 要解决的问题

传统静态检查擅长发现格式、类型和已知规则问题，但难以理解一次改动是否破坏了业务逻辑、错误处理或跨文件调用关系。ReviewAgent 的目标是在 PR 创建或更新后：

1. 获取并解析本次代码变更；
2. 为每组相关变更提供必要的仓库上下文；
3. 让 Agent 调用受控工具完成审查；
4. 只将证据充分、能落到具体改动行的建议回写到 GitHub；
5. 记录审查过程、成本和结果，便于复盘和评估。

### 1.2 MVP 不做什么

- 不做完整的 GitHub / GitLab 多平台支持（首版只支持 GitHub）。
- 不做面向所有语言的通用审查（首版优先 Go，随后可扩展 TypeScript/Python）。
- 不把整个仓库直接放入模型上下文，也不让模型拥有任意 Shell 或网络访问权限。
- 不追求替代人工 Review；只输出高置信度、可行动的补充意见。
- 不复刻 OpenCodeReview 的全部功能或源码；借鉴其“确定性流水线 + 受控 Agent”的设计思想，并独立实现接口、数据结构和策略。

## 2. 核心设计原则

| 原则 | 实践方式 |
| --- | --- |
| 确定性优先 | Diff 解析、文件筛选、行号定位、去重、预算和 GitHub 回写由普通程序完成。 |
| Agent 负责判断 | LLM 负责理解意图、探索上下文、提出潜在缺陷；不直接决定权限和最终发布。 |
| 证据驱动 | 每条评论必须关联新增行、代码片段及推理依据；无法定位或证据不足则丢弃。 |
| 有限上下文 | 以语义变更组为审查单位，按需读取文件和搜索符号，限制工具调用、Token 与时长。 |
| 可降级、可观测 | 单个变更组失败不影响其他组；每次运行都可查看状态、工具轨迹、耗时与成本。 |

## 3. 总体工作流

```text
GitHub PR / synchronize
        |
        v
Webhook 验签与任务创建
        |
        v
获取 PR Diff -> 文件筛选 -> 语义分组
        |                         |
        |                         v
        |                 并发 Group Review Agent
        |                    |- read_file
        |                    |- search_code
        |                    |- read_diff
        |                    `- finish
        v
Finding 汇总 -> 校验/去重 -> 行号定位 -> 风险过滤
        |
        v
GitHub Review Comment / Review Summary
        |
        v
运行记录、指标与可追溯审计
```

## 4. 功能范围与实施阶段

### Phase 0：开发基础设施

目标：建立可重复的本地开发与测试环境，为后续模块提供稳定边界。

- 配置管理：GitHub App、Webhook Secret、LLM Provider、模型、预算等配置从环境变量或配置文件读取；敏感信息不入库。
- 容器化：使用 Docker Compose 拉起服务与必要依赖（建议 PostgreSQL；Redis 可在异步任务引入后加入）。
- 基础数据模型：`ReviewRun`、`ChangeGroup`、`Finding`、`ToolCall`、`ReviewComment`。
- 本地回放：保存或加载 fixture Diff，使 Agent 流程不依赖真实 GitHub PR 即可调试。
- 可观测性：结构化日志、请求 ID、运行耗时和 LLM Token/费用统计。

验收：可用一份固定 Diff 在本地启动一次模拟审查，并得到完整的运行记录。

### Phase 1：MVP——GitHub PR 自动审查闭环

目标：跑通“PR 触发 → Diff 审查 → PR 评论”的最小可演示产品。

#### 1. GitHub 接入

- 接收 `pull_request.opened`、`pull_request.reopened`、`pull_request.synchronize` Webhook。
- 校验 Webhook 签名，解析仓库、PR 编号、base/head SHA 与安装信息。
- 使用 GitHub App Installation Token 调用 API，避免使用个人 Token。
- 拉取 PR 文件列表、patch、变更行位置和 PR 元数据。
- 以 PR 的 head SHA 作为幂等键：相同提交不重复发布评论。

#### 2. Diff 解析与文件筛选

- 将 patch 解析成文件、hunk、旧/新行号与新增/删除行。
- 默认忽略二进制文件、锁文件、生成文件、vendor、依赖目录和超大文件。
- 支持仓库级 `.reviewagent.yml`：配置忽略路径、最大文件数、最大 Diff 行数和语言范围。
- 对超限 PR 给出明确的降级策略：仅审查高优先级文件或只生成摘要。

#### 3. 单轮 Diff Review Agent

- 为每个可审查文件构造最小提示词：PR 元数据、文件路径、Diff、审查准则与结构化输出约束。
- 要求模型输出 JSON Finding：`title`、`body`、`severity`、`confidence`、`file`、`new_line`、`evidence`、`suggestion`。
- 首版只识别：正确性缺陷、异常/边界处理、明显安全风险、并发/资源泄漏；不评论格式或个人偏好。
- 将结果转换为 GitHub inline review comment；不能映射至新增行的 Finding 不发布。
- 同时发布一条 PR Summary：审查范围、发现数、跳过原因与运行链接/ID。

验收：对准备好的 Go 示例 PR，系统可自动发表评论；评论均指向新增行，且重试不会产生重复评论。

### Phase 2：仓库上下文与受控工具循环

目标：解决“只看 Diff 不懂上下文”的核心缺陷，让 Agent 能按需验证假设。

#### 1. 安全的仓库快照

- 将目标 commit 以只读方式获取到临时工作区，限制大小、超时和并发。
- 明确禁止 Agent 调用任意 Shell；工具层只暴露经过参数校验的只读能力。
- 清理临时工作区，并记录 commit SHA，确保审查结论可复现。

#### 2. Agent 工具集

| 工具 | 用途 | 关键限制 |
| --- | --- | --- |
| `read_file(path, start_line, end_line)` | 阅读定义、调用方或配置 | 路径必须在仓库内；限制行数/字节数。 |
| `search_code(query, path?)` | 查找符号、错误码、配置使用点 | 限制结果数量与搜索范围。 |
| `read_diff(path?)` | 回看当前 PR 的变更 | 只返回本次 PR 已解析的 Diff。 |
| `list_files(path?)` | 理解有限目录结构 | 限制深度和条目数。 |
| `finish(findings)` | 提交结构化审查结果 | 必须通过 schema 校验。 |

#### 3. 工具调用循环与预算

- 模型提出工具调用，服务执行并返回结果，直到 `finish` 或达到限制。
- 为每个审查单元设置 `max_tool_calls`、`max_input_tokens`、`max_output_tokens`、`max_duration`。
- 记录每次调用的参数摘要、耗时、结果大小和失败原因。
- 工具失败时返回可解释错误，允许 Agent 换一种方式继续；超过预算则优雅结束并标注不完整状态。

验收：Agent 能针对一个函数改动主动读取其调用方或错误处理路径，并基于读取到的证据输出评论。

### Phase 3：语义分组、并行调度与评论质量控制

目标：在中等规模 PR 上提高上下文相关性、吞吐和评论可信度。

#### 1. 语义变更分组

- 基于路径、导入关系、符号名和 Diff 邻近性，将相关文件归为 `ChangeGroup`。
- 独立变更（例如文档、测试、配置）可单独成组；跨文件重构应归入同一组。
- 每组由一个 Agent 独立审查，失败隔离，不阻塞其他组。
- 第一版规则可解释、可配置；后续再尝试 LLM 辅助分组并与规则结果对比。

#### 2. 并行任务调度

- 通过任务队列执行 Group Review，配置最大并发和每仓库并发上限。
- 任务具备状态机：`queued → running → succeeded / partial / failed / cancelled`。
- 失败采用有限重试和退避；LLM 限流、GitHub API 限流和超时分别处理。
- PR 新提交到来时，可取消旧 head SHA 的未开始任务。

#### 3. Finding 校验与过滤

- Schema 校验：必填字段、合法 severity、文件存在、行号属于新增行。
- 证据校验：评论涉及的函数/变量/行为须能在 Diff 或工具结果中找到。
- 去重：基于路径、行号、规则类别和语义相似度合并重复意见。
- 置信度门槛：低置信度 Finding 不发 inline comment，可放入摘要的“需人工确认”区。
- 风险分级：`critical`、`high`、`medium`、`low`；首版建议仅发布 `high` / `medium`。

#### 4. 评论重定位

- 优先使用 GitHub Diff position / 新文件行号生成 inline comment。
- 若因为 PR 更新而失效，使用代码片段锚点和邻近上下文重新定位。
- 无法可靠重定位时不强行评论，改为 PR Summary 中的普通建议。

验收：一个包含多文件改动的 PR 可被分组并行审查；重复、无法定位或缺乏证据的意见不会发布。

### Phase 4：静态信号、测试和仓库规则融合

目标：让 Agent 以真实工程信号为依据，而不只依赖语言模型判断。

- 静态分析适配器：先接入 Go 的 `go vet` / `staticcheck`，将诊断统一为标准 Finding 格式。
- 测试适配器：受限资源下执行目标测试或读取 CI 状态；测试执行必须隔离并设置白名单、超时、CPU/内存限制。
- 仓库规则：支持 `.reviewagent.yml` 定义业务禁用 API、敏感目录、必须检查项和 severity 覆盖规则。
- 结果融合：确定性工具的诊断直接进入校验层；Agent 可读取摘要，以减少重复建议、解释影响范围。
- 代码建议：仅在变更很小、替换范围明确且模型置信度足够时生成 GitHub suggestion，默认不开启自动修改。

验收：对故意引入的静态分析问题，系统可稳定发现并在 PR 上以统一格式呈现；Agent 不会重复同一问题。

### Phase 5：评估、成本控制与产品化体验

目标：证明项目不仅“能跑”，还能被客观评估、持续改进。

- 评估集：构建包含真实或手工注入缺陷的 PR fixture，并标注期望 Finding 与不应评论的样本。
- 指标：Comment Precision、Bug Recall、错误定位率、重复率、平均延迟、每 PR Token/成本、人工采纳率。
- A/B 实验：比较 `只看 Diff`、`Diff + 工具`、`Diff + 工具 + 校验` 三种策略的质量、延迟和成本。
- Review Budget：按仓库/PR 设置文件数、变更组数、工具次数、Token、最长运行时长；超限时有可解释降级。
- 运行查看页或 CLI：展示审查状态、分组、工具轨迹、跳过原因、Finding 过滤原因和最终评论。
- 人工反馈：支持对评论标记“有用 / 无用”，用于统计与后续提示词或规则改进。

验收：能够输出一份评估报告，量化上下文工具和校验流程对准确性、成本与延迟的影响。

## 5. 关键数据契约

### 5.1 Finding（Agent 与后处理层的边界）

```json
{
  "title": "错误被吞掉，调用方无法感知失败",
  "body": "此处忽略了 Save 返回的错误；当数据库写入失败时接口仍会返回成功。",
  "severity": "high",
  "confidence": 0.88,
  "file": "internal/order/service.go",
  "new_line": 42,
  "evidence": ["Diff 第 42 行丢弃了 err", "调用方未做其他错误检查"],
  "suggestion": "返回或处理 Save 的错误。"
}
```

说明：Finding 只是候选结果，不能直接发布。它必须通过行号、证据、去重和风险过滤。

### 5.2 运行状态

```text
received -> validated -> diff_ready -> grouped -> reviewing
         -> validating -> publishing -> completed
                                   \-> partial / failed / cancelled
```

## 6. 建议的开发顺序（前 6 周）

| 周期 | 可交付物 | 重点 |
| --- | --- | --- |
| 第 1 周 | 项目骨架、GitHub App 测试仓库、Webhook 验签、Diff fixture | 接口边界与本地可回放。 |
| 第 2 周 | Diff 解析、筛选、单文件 LLM 审查、结构化 Finding | 先让模型稳定输出可处理数据。 |
| 第 3 周 | GitHub inline comment、幂等、PR summary、日志 | 打通 MVP 演示闭环。 |
| 第 4 周 | 仓库快照、`read_file` / `search_code`、预算控制 | 形成真正的 Agent 工具循环。 |
| 第 5 周 | 变更分组、并发调度、校验/去重/重定位 | 解决中型 PR 的质量和工程性。 |
| 第 6 周 | Go 静态分析、评估集、指标报告、演示材料 | 形成可写进简历的差异化证据。 |

## 7. 首版验收清单

- [ ] GitHub App 能安全接收并验证 PR Webhook。
- [ ] 能从指定 head SHA 获取并解析 Diff，正确识别新增行。
- [ ] 忽略规则和规模上限可配置，超限有可解释降级。
- [ ] LLM 输出严格遵循 Finding schema，异常输出不会导致整个任务失败。
- [ ] 高置信度 Finding 能以 inline comment 回写到准确新增行。
- [ ] 同一 commit 重复触发不会生成重复评论。
- [ ] 每次运行可查看状态、耗时、模型用量、工具调用和过滤原因。
- [ ] 至少有一组固定 PR fixture 与端到端测试，覆盖成功、超限、模型失败和 GitHub API 失败。

## 8. 项目亮点与后续复盘问题

完成后，项目需要能回答以下问题，而不仅是展示调用了 LLM：

1. 为什么 Diff 解析、行号定位、预算控制不交给 Agent？
2. 为什么要以变更组而不是整个 PR 作为上下文边界？
3. Agent 能读哪些信息，为什么这些工具不会造成路径逃逸或成本失控？
4. 怎样避免幻觉评论、重复评论和失效行号？
5. `只看 Diff` 与 `带仓库上下文` 的质量和成本差异是多少？
6. 在模型、GitHub 或任务队列失败时，如何做到局部失败、可重试、可追踪？

这些问题的答案，以及评估数据和一次完整的公开演示，将构成项目区别于“调用 API 做代码审查”的核心价值。
