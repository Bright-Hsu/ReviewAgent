# ReviewAgent

一个面向 GitHub Pull Request 的 Go 版 AI Code Review MVP。当前版本实现了：Webhook 验签、PR Diff 获取、文件过滤、OpenAI 兼容模型的结构化 Finding、确定性的新增行/置信度校验、去重，以及 GitHub 行内评论和汇总评论。

模型可在一次审查中受控调用下列工具，且全部固定到当前 PR 的 head commit：

- `read_file`：读取一个小的代码行范围，最多 160 行 / 24 KiB；
- `read_diff`：重读本次 PR 的一个文件补丁；
- `list_files`：最多列出 100 个仓库文件；
- `search_code`：在最多 30 个、总计 120 KiB 的源码文件中检索，最多返回 12 条匹配。

每次审查最多 4 次工具调用。Agent 不能运行命令、写文件或请求任意 URL；路径逃逸和过大文件会被拒绝。

## 本地启动

1. 复制 `.env.example` 中的变量到你的终端（不要提交真实密钥）。
2. 配置 `WEBHOOK_SECRET`、`GITHUB_TOKEN`、`LLM_API_KEY`。开发期 GitHub token 至少需要目标仓库的 Pull requests 读写权限。
3. 执行：

```powershell
go test ./...
go run ./cmd/reviewagent
```

健康检查地址为 `GET /healthz`，Webhook 地址为 `POST /webhooks/github`。将该地址配置到 GitHub App 后，订阅 `Pull requests` 的 `opened`、`reopened`、`synchronize` 事件。

Webhook 验签后会将审查任务持久化到 `DATABASE_URL` 指向的 SQLite 数据库，由单进程后台 worker 执行。worker 使用租约和心跳恢复崩溃任务；临时错误最多重试 3 次，新 head SHA 会取消同一 PR 的旧任务。状态包括 `queued`、`running`、`completed`、`partial`、`failed` 和 `cancelled`。评论带稳定标记，任务重试时会跳过已发布的评论。可通过下列接口查询单次运行；生产环境务必设置随机的 `ADMIN_TOKEN`：

```powershell
Invoke-RestMethod -Headers @{ Authorization = "Bearer $env:ADMIN_TOKEN" } `
  http://localhost:8080/runs/<run-id>
```

## 本地 Diff 回放

使用模型配置验证从 Diff 到结构化 Finding 的主链路，不会访问 GitHub：

```powershell
go run ./cmd/reviewagent -fixture ./fixtures/error_ignored.diff
```

未配置 `LLM_API_KEY` 时，服务以安全的 dry-run 模式启动，不会生成或发布模型评论。

## 现在已实现 / 后续方向

- 已实现：Phase 1 的单文件审查闭环、受控仓库上下文，以及持久化任务状态机。
- 下一步：GitHub App installation token、任务队列/重试、变更分组与可观测性页面。

详细规划见 [功能规划](docs/code-review-agent-feature-plan.md)。
