# edoc-edualc

Go 重写的 Claude Code(src:../claude-code)，支持多模型（Anthropic + OpenAI），通过 Web API 访问。
## 构建

```bash
cd backend
go build ./...
go build -o edoc ./cmd/edoc
```

## 运行

```bash
# CLI
edoc                          # 交互式 REPL
edoc -p "explain this code"   # 单次执行
edoc serve                    # Web API server (port 8080)
edoc --config path/application.yml  # 指定配置文件

# 环境变量（优先级高于配置文件）
ANTHROPIC_API_KEY             # Anthropic API key
OPENAI_API_KEY                # OpenAI API key
ANTHROPIC_BASE_URL            # 自定义 Anthropic base URL
OPENAI_BASE_URL               # 自定义 OpenAI base URL
EDOC_MODEL                    # 模型 (默认 claude-sonnet-4-20250514)
EDOC_PROVIDER                 # anthropic 或 openai (默认 anthropic)
EDOC_SERVER_PORT              # server 端口 (默认 8080)
EDOC_PROFILE                  # 激活的 profile (默认读 application.yml 中的 profile 字段)
DATABASE_URL                  # PostgreSQL 连接串 (优先级最高)
EDOC_DATABASE_HOST/PORT/USER/PASSWORD/DBNAME  # 分项 PG 配置
```

## 配置


```
backend/resources/
├── application.yml           # 基础配置 + profile: dev（提交到 git）
├── application-dev.yml       # Dev 敏感配置（gitignored，复制 example 填写）
├── application-prod.yml      # Prod 配置（gitignored）
└── application-example.yml   # 模板（提交到 git）
```

配置优先级: 环境变量 > application-{profile}.yml > application.yml > 默认值

## API

```
POST /api/chat                # SSE 流式对话
POST /api/compact             # SSE 流式上下文压缩
POST /api/command             # 执行 slash 命令 (JSON)
POST /api/sessions/:id/compact # SSE 流式压缩指定 session
GET  /api/models              # 可用模型列表
GET  /health                  # 健康检查
```

## 项目结构

```
backend/
├── cmd/edoc/main.go              # 入口: REPL / 单次 / server
├── db/migrations/                # SQL 迁移文件
├── internal/
│   ├── agent/
│   │   ├── loop.go               # 核心 agent loop (对标 query.ts:queryLoop)
│   │   ├── state.go              # Config, State, Event 类型
│   │   └── subagent.go           # SubagentResolver — 子代理执行器
│   ├── api/
│   │   ├── handler.go            # SSE handlers
│   │   ├── router.go             # Gin 路由
│   │   └── middleware.go         # CORS
│   ├── compact/
│   │   ├── compact.go            # Full compact (LLM 摘要)
│   │   ├── micro.go              # Microcompact (清空旧 tool_result)
│   │   └── prompt.go             # 9 段式摘要 prompt
│   ├── command/
│   │   └── command.go            # Slash 命令逻辑 (REPL + Web API 共享, 返回 string+error)
│   ├── config/
│   │   └── config.go             # Viper 配置加载 (YAML + ENV)
│   ├── db/
│   │   ├── db.go                 # pgxpool 连接管理
│   │   └── migrate.go            # 自动迁移
│   ├── hook/
│   │   ├── hook.go               # Hook 类型定义 + JSON 协议解析 + async 响应
│   │   ├── settings.go           # .edoc/settings.json 加载
│   │   └── runner.go             # Hook 执行引擎 (command/http/prompt types)
│   ├── lsp/
│   │   ├── types.go              # LSP 类型定义 + JSON-RPC 协议类型
│   │   ├── client.go             # JSON-RPC client over stdio (Content-Length 帧)
│   │   ├── manager.go            # 多 server 管理 + 按扩展名路由 + 文件同步
│   │   └── settings.go           # .edoc/settings.json lsp_servers 加载
│   ├── mcp/
│   │   ├── manager.go            # MCP 连接管理 + 工具发现 (stdio/SSE)
│   │   └── tool.go               # MCPTool 适配器 + RegisterTools
│   ├── memory/
│   │   ├── store.go              # MemoryStore interface
│   │   ├── memory.go             # PostgreSQL 实现
│   │   ├── write.go              # 文件写入实现 (fallback)
│   │   ├── scan.go               # 记忆扫描
│   │   └── prompt.go             # 记忆注入 system prompt
│   ├── message/
│   │   ├── types.go              # 统一 Message 类型
│   │   └── convert.go            # 内部消息 ↔ 各厂商 API 格式
│   ├── provider/
│   │   ├── provider.go           # Provider interface + 错误分类
│   │   ├── anthropic.go          # Anthropic SDK 封装
│   │   └── openai.go             # OpenAI SDK 封装
│   ├── prompt/
│   │   └── system.go             # System prompt 组装 + skill system-reminder 注入
│   ├── session/
│   │   └── store.go              # Session 持久化 (PostgreSQL) + ReplaceMessages (compact)
│   ├── skill/
│   │   ├── skill.go              # Skill 类型 + .md 文件解析 (frontmatter)
│   │   ├── registry.go           # Skill 注册表 (Register 不覆盖用户 skill)
│   │   ├── builtin.go            # 内置 skill (commit, review)，低于用户定义优先级
│   │   └── dirs.go               # 默认扫描目录 + BuildSystemReminderSection
│   ├── task/
│   │   ├── task.go               # 后台任务类型 (Task, TaskStatus, TaskNotification)
│   │   ├── manager.go            # TaskManager — 生命周期管理 + 通知 channel
│   │   └── shell_task.go         # LocalShellTask — 后台 shell 命令执行
│   ├── team/
│   │   ├── task.go               # Team/Member 核心类型
│   │   └── manager.go            # TeamManager 实现 — 团队生命周期 + teammate goroutine 消息泵
│   ├── token/
│   │   └── estimate.go           # Token 估算 (~4 bytes/token)
│   └── tool/
│       ├── tool.go               # Tool interface + Result 类型
│       ├── registry.go           # 工具注册表 + DefaultRegistry
│       ├── permission.go         # 权限模式 + PermissionCallback
│       ├── context.go            # AgentIdentity context (context.WithValue)
│       ├── team_ref.go           # TeamManager interface + TeamBrief/MailboxMessage 类型 (解耦)
│       ├── team_create.go        # TeamCreateTool
│       ├── team_delete.go        # TeamDeleteTool
│       ├── send_message.go       # SendMessageTool — agent 间消息 + 广播
│       ├── agent.go              # AgentTool + AgentResolver interface
│       ├── skill.go              # SkillTool — inline 执行 + $ARGUMENTS 替换
│       ├── webfetch.go           # WebFetchTool — HTTP 抓取 + html-to-markdown
│       ├── webfetch_provider.go  # WebFetchProvider 适配器 (二次模型摘要)
│       ├── bash.go               # 命令执行 (PowerShell/Bash/cmd + GBK 转码) + run_in_background
│       ├── read.go               # 文件读取
│       ├── write.go              # 文件写入
│       ├── edit.go               # 文件编辑 (5 级模糊匹配)
│       ├── glob.go               # 文件搜索
│       ├── grep.go               # 内容搜索
│       ├── task_ref.go           # TaskStarter/TaskOutputReader/TaskStopper 接口 (解耦)
│       ├── task_output.go        # TaskOutputTool — 读取后台任务输出 (blocking/非阻塞)
│       ├── task_stop.go          # TaskStopTool — 停止后台任务
│       ├── lsp.go                # LSPTool — goToDefinition/findReferences/hover/documentSymbol
│       └── worktree.go           # EnterWorktree/ExitWorktree — git worktree 隔离
├── resources/
│   ├── application.yml           # 基础配置 + profile 激活（提交到 git）
│   ├── application-dev.yml       # Dev 敏感配置（gitignored）
│   └── application-example.yml   # 配置模板（提交到 git）
```

```
frontend/                         # Web 前端 (Vite + React + TypeScript + Tailwind)
├── src/
│   ├── App.tsx                   # 根组件: 会话管理 + 布局
│   ├── api.ts                    # API 层: fetchSessions/streamChat/streamSessionChat/executeCommand/compactSession
│   ├── types.ts                  # 类型定义: Session, Message, SSEEvent, Model
│   ├── index.css                 # 全局样式 (Tailwind + prose/scrollbar)
│   └── components/
│       ├── Sidebar.tsx           # 左侧会话列表 + 新建按钮
│       ├── ChatArea.tsx          # 聊天区: SSE 流式渲染 + 工具调用展示 + slash 命令拦截路由
│       ├── MessageBubble.tsx     # 消息气泡 (user/assistant + tool calls)
│       ├── ChatInput.tsx         # 输入框 (自动扩展 + / 命令自动补全菜单 + Stop 按钮)
│       └── Markdown.tsx          # Markdown 渲染 (react-markdown + remark-gfm)
├── vite.config.ts                # Vite 配置 + /api 代理到 :8080
└── package.json
```

## 架构决策

- **Provider 抽象**: `StreamChat` 返回 `<-chan StreamEvent`，统一 Anthropic/OpenAI 流式响应
- **Agent Loop**: while 循环，每轮: microcompact → autocompact → API 调用 → tool 执行 → 下一轮
- **Skill 系统**: `AgentTool` + `AgentResolver` interface 解耦循环依赖；`subagentResolver` 继承父 Config，子代理不持久化 session
- **Skill 系统**: 扫描 `.claude/skills/*.md`，解析 frontmatter，SkillTool inline 执行（注入内容到对话）+ `$ARGUMENTS` 替换，skill 列表注入 system-reminder；内置 skill（commit, review）优先级低于用户定义
- **命令共享**: REPL slash 命令逻辑抽取到 `command` 包，返回 `(string, error)` 无 side effect；REPL 和 Web API 共享同一实现，`POST /api/command` 供前端调用
- **前端 Slash 命令路由**: `/clear`/`/help`/`/model`/`/new` 纯前端处理；`/compact` SSE 流式；`/commit`/`/review` 走模型 skill 系统（自然语言 prompt 触发 SkillTool）；其余走 `POST /api/command`
- **错误恢复**: 4 种恢复策略 — 模型降级(max_output_tokens) / 限流降级(rate_limit) / 紧凑恢复(prompt_too_long) / 轮数限制(maxTurns)
- **Tool 并发**: 安全工具并行执行，不安全工具串行（对标 toolOrchestration.ts）
- **Edit 匹配**: 5 级策略 — 精确 → 弯引号归一化 → 去尾部空格 → 缩进归一化（tabs↔spaces 双向）→ 组合；Write 对 .go 文件自动将 spaces 转 tabs
- **编码处理**: Windows 命令输出自动检测 UTF-8/GBK，GBK → UTF-8 转码
- **Shell 选择**: Windows 自动检测 pwsh > powershell > bash > cmd
- **配置管理**: Viper (YAML + 环境变量覆盖)，类似 Spring Boot application.yml
- **MCP 客户端**: `mcp-go` SDK，stdio/SSE 两种 transport，启动时并发连接所有 server，工具以 `mcp__<server>__<tool>` 命名注入 registry；无 OAuth 支持
- **数据库**: pgx 直连 PostgreSQL，自动迁移；无 DB 时降级为文件存储
- **不引入 CLI 框架**: 直接用 os.Args，保持简单
- **LSP 集成**: 内置 LSP client（JSON-RPC 2.0 over stdio），`lsp.Manager` 管理多 server 按扩展名路由，懒启动；`LSPTool` 暴露 goToDefinition/findReferences/hover/documentSymbol 给模型；Edit/Write 后自动 didChange/didSave 通知；配置在 `.edoc/settings.json` 的 `lsp_servers` 字段
- **Hooks 系统**: `.edoc/settings.json` 加载配置，`hook.Runner` 执行 command/http/prompt hooks；agent loop 集成点（PreToolUse/PostToolUse/PostToolUseFailure/PermissionDenied/PreCompact/PostCompact/Stop 在 loop.go，UserPromptSubmit 在 main.go REPL）；JSON stdin 传入 HookInput，stdout 解析 decision/updatedInput/additionalContext；exit code 2 = blocking error；once 标志、asyncRewake、per-hook shell 覆盖
- **后台任务系统**: `task.Manager` 管理后台 shell 命令；BashTool 的 `run_in_background` 参数启动后台任务；agent loop 非阻塞检查通知 channel，任务完成时注入 `<system-reminder>` 消息；`TaskOutputTool` 读取输出（blocking/非阻塞），`TaskStopTool` 终止任务；tool 包定义接口（TaskStarter/TaskOutputReader/TaskStopper），task 包实现，避免循环导入
- **多代理协作 (Swarm)**: `team.Manager` 管理团队生命周期和 in-process teammate goroutines；`TeamCreateTool`/`TeamDeleteTool` 创建/解散团队；`SendMessageTool` 实现 agent 间消息传递和广播；`AgentTool` 扩展支持 teammate 派生（name + team_name 参数）；teammate 以 goroutine 运行，内含消息泵（run agent loop → idle → wait for inbox message → repeat）；channel 通信替代文件 mailbox；`tool.TeamManager` interface 解耦 tool↔team 避免循环导入；agent loop step 1.6 非阻塞检查 `TeamInbox`，teammate 消息注入为 `<teammate-message>` XML；`context.WithValue` 存储 agent identity 替代 AsyncLocalStorage

## 多模型架构影响分析

> 本项目目标连接 GLM、DeepSeek、Qwen 等国产模型。
> **核心风险**: RL 冗长推理 + 稀疏注意力注意力丢弃，这两个架构趋势对 coding agent 场景构成直接威胁。

### 风险一: RL 训练导致的推理冗长病

#### 问题本质

[DeepSeek-R1](https://arxiv.org/abs/2501.12948) 证明了纯 RL 可以涌现推理能力，但代价是 **reward bias 导致的冗长倾向**:

- R1 的 RL reward 机制存在隐含偏差: "wrong but long gets punished less than wrong and short"（错但长比错但短惩罚少）
- 模型学会了"不确定就多写"的策略，推理链动辄 **数千 token**
- R1 论文中著名的 "aha moment"（自我回溯 "等等，这个方法不对"）虽然提升了准确率，但产出了大量冗余推理
- 后续国产模型（GLM-Z1、Qwen3 thinking mode）全部沿用了这条路线，冗长病成为行业通病
- 已有研究证明了 [Excessive Reasoning Attack](https://openreview.net/pdf?id=g0uBExrnjz)：攻击者可以故意触发超长推理链

#### 对 Coding Agent 的直接影响

| 场景 | 影响 |
|------|------|
| 简单文件读取 | 模型先推理 2000 tokens "为什么要读这个文件" → 延迟 40-67s 才发出 Read tool call |
| 代码修改 | 每轮 tool call 前都有冗长 reasoning → 10 轮 tool call 可能产生 20000+ reasoning tokens |
| 延迟感知 | 用户看到 "thinking..." 40 秒无任何动作 → 体验远差于 Claude 的秒级响应 |
| Token 放大效应 | reasoning tokens 在多轮对话中回放 → 对话膨胀速度远超 Claude（Claude thinking blocks 不计入后续上下文） |
| 成本 | 即使 input token 便宜 ($0.28/M)，reasoning 回放导致每轮输入膨胀 → 多轮后成本非线性增长 |

#### 需要的系统级防御

1. **Thinking Token Budget**: per-model 配置最大 thinking token 数，超限截断 reasoning 输出
2. **reasoning 去重回放**: 多轮对话中，历史 reasoning_content 不原样回放，而是压缩为摘要或直接丢弃
3. **Thinking 模式分级**: "快速模式"（禁用 thinking）vs "深度模式"（启用 thinking），根据任务复杂度自动切换
4. **流式 reasoning 展示**: 前端实时展示 reasoning 过程（而非等待），让用户知道系统在工作
5. **Early Tool Call 打断**: 如果 reasoning 阶段已经产生明确的 tool call 意图，尽早发出 tool call 而非等 reasoning 结束

**代码影响**:
- `agent/state.go`: 新增 `MaxThinkingTokens` per-model 配置
- `provider/openai.go`: 流式 delta 中提取 `reasoning_content`，超限时截断
- `compact/compact.go`: reasoning_content 压缩策略（丢弃/摘要/保留）
- `message/types.go`: 统一 Anthropic `thinking` block + OpenAI `reasoning_content`

### 风险二: 稀疏注意力对 System Prompt 的静默丢弃

#### 问题本质

DeepSeek DSA/GLM-5 DSA 的 Lightning Indexer 动态决定每个 token 应关注哪些前序 token。在 coding 场景下，这导致一个致命风险: **系统指令被判定为"不重要"而被注意力机制静默忽略**。

**技术机理**:

1. **Attention Sink 现象** ([StreamingLLM](https://arxiv.org/abs/2309.17453), [ICLR 2025](https://proceedings.iclr.cc/paper_files/paper/2025/file/f1b04face60081b689ba740d39ea8f37-Paper-Conference.pdf)): 模型将大量注意力分数集中在前几个 token（通常是 BOS token），形成"注意力黑洞"
2. **[Cerebras 实证](https://www.cerebras.ai/blog/compressing-kv-cache-memory-by-half-with-sparse-attention)**: 保留前 512 个 attention sink token 对模型质量至关重要；但 Claude Code 的 system prompt 远超 512 tokens
3. **System Prompt 已被验证会退化**: [Reddit 讨论](https://www.reddit.com/r/ClaudeAI/comments/1rh5l0l/system_prompt_compliance_degrades_over_long/) 和多篇论文确认，system prompt 遵循度随上下文增长而退化；稀疏注意力会加速这一退化
4. **"Lost in the Middle" 放大**: 稀疏注意力本质上是在做 "哪些 token 可以被忽略" 的决策；coding 场景中，具体的代码内容天然获得高注意力，而通用指令（安全规则、工具格式）则被边缘化

#### Coding Agent 场景下的具体风险

```
System Prompt 布局 (典型 ~8000 tokens):
┌──────────────────────────────────────────────┐
│  <attention_sink_zone> ← 模型会关注 (前512)  │
│  项目描述、核心规则                             │
├──────────────────────────────────────────────┤
│  <工具 schema 区域> ← 高风险被丢弃 (~5000 tok) │
│  Bash, Read, Write, Edit, Glob, Grep...      │
│  (JSON schema, 低语义密度, 重复格式)           │
├──────────────────────────────────────────────┤
│  <安全规则> ← 高风险被丢弃 (~1000 tok)        │
│  "不要引入 XSS"、"不要 SQL 注入"...           │
│  (通用指令，与当前代码任务相关性低)             │
├──────────────────────────────────────────────┤
│  <用户代码上下文> ← 模型会关注 (高相关性)      │
│  具体的代码文件、函数定义、错误信息             │
└──────────────────────────────────────────────┘
```

**风险表现**:
- 模型 "忘记" tool calling 格式，输出自由文本而非结构化 tool call
- 安全规则被忽略（SQL 注入、XSS 等防护失效）
- 编辑操作不遵循 Edit tool 的 5 级匹配规则，自行发明格式
- 越长的对话历史 → 越多代码上下文竞争注意力 → 系统指令被进一步边缘化

#### 需要的系统级防御

1. **Tool Schema 按需注入 (ToolSearchTool 价值重定义)**:
   - 原版 Claude Code 的 ToolSearchTool 是为了省 input token 成本
   - 在稀疏注意力场景下，它的价值变成了 **减少 system prompt 体积以提高指令遵从率**
   - 只注入当前任务相关的 3-5 个工具 schema，而非全部 22 个
   - **这从成本优化升级为可靠性保障**

2. **关键指令重复/锚定**:
   - 在用户消息中重复核心规则（而非只在 system prompt 开头说一次）
   - 利用 attention sink 特性，将最关键的约束放在用户消息末尾（最近的位置）
   - 对 safety-critical 指令（"不要删除文件"等）采用 `<important>` 标签强化

3. **System Prompt 压缩**:
   - Tool schema 从完整 JSON 压缩为只保留 name + description + required params
   - 安全规则合并为简洁的 bullet points
   - 定期测试各模型在不同上下文长度下的指令遵从率

4. **Per-Model 测试基准**:
   - 建立 "长上下文指令遵从测试集"：在不同 context window 填充度下测试模型是否仍遵循 tool 格式、安全规则
   - 标记哪些模型在超过 N tokens 后开始丢失指令
   - 根据测试结果动态调整 compact 触发阈值

**代码影响**:
- `prompt/system.go`: system prompt 组装策略需支持 "精简模式"（只注入必要内容）
- `tool/registry.go`: 新增 "按需加载" 接口，根据任务类型动态选择注入哪些工具 schema
- `compact/compact.go`: compact 触发条件从 "token 数量阈值" 变为 "模型指令遵从测试结果"
- `provider/` 层: per-model 的 system prompt 策略配置（哪些模型需要精简、哪些可以全量）

### 风险三: Engram 知识固化 — 模型对过时 API 的"高置信幻觉"

#### 问题本质

[DeepSeek Engram](https://arxiv.org/abs/2601.07372) (Conditional Memory via Scalable Lookup) 引入了 hash-based N-gram 查找表，将静态知识（命名实体、惯用表达、API 模式等）从推理计算中剥离，存入 O(1) 查找的 embedding table:

- Engram 在 **预训练阶段** 将 N-gram 模式写入 embedding table（如 `torch.nn.functional.relu(` → 对应的 API 签名 embedding）
- 推理时通过 context-aware gating 机制决定是否采纳查找结果: 如果 lookup embedding 与当前 hidden state 语义对齐，gate α_t → 1（高置信采纳）
- 论文实验证实: 剥离 Engram 后，事实性知识 benchmark **暴跌至 29-44%**（TriviaQA 仅 29%） — 说明大量知识已从 FFN 迁移到 Engram
- 可能成为 **DeepSeek V4 架构** 的核心组件，后续国产模型很可能跟进

#### 对 Coding Agent 的致命影响

**核心矛盾**: Engram 的设计哲学是 "简单知识查表，复杂推理交给 attention"。但 coding 场景中，"查表"的知识可能是 **过时/错误** 的:

```
训练时 (假设 2025Q2):                    推理时 (2026Q4):
PyTorch 2.9 API 写入 Engram:             项目使用 PyTorch 2.19:
  torch.nn.functional.xxx(                  torch.nn.functional.xxx(
    input,                                     input,        # 不变
    dim=-1           ← 固化到 Engram          dim=-1,        # 不变
  )                                            new_required_param  # 2.19 新增必填！
                                             )
```

**为什么这比普通幻觉更危险**:

| 维度 | 普通幻觉 | Engram 固化幻觉 |
|------|---------|----------------|
| 置信度 | 中等（模型可能犹豫） | **极高**（lookup gate α→1，模型 "确信" 自己知道） |
| 纠正难度 | context 中提供正确文档通常可以纠正 | Engram 的 gate 基于 N-gram hash 匹配，即使 context 中有正确 API，lookup 仍然触发 |
| 影响范围 | 个别错误 | **同版本的所有 API 调用** 全部使用旧签名 |
| 检测难度 | 用户可能注意到模型不确定 | 模型输出极其自信，用户容易信任 |

**受影响场景**（不限于 PyTorch）:

- **框架 API 变更**: React hooks 签名、Go 标准库函数、Kubernetes API 版本
- **语言版本差异**: Python 3.11 vs 3.14 的 `typing` 模块、Go 1.22 vs 1.24 的 `range` 语义
- **包版本差异**: npm 包 major version 升级后的 breaking change
- **平台差异**: 同一库在 CPU/GPU/TPU 上的不同 API
- **内部/私有 API**: Engram 完全不可能有（不在训练数据中），但模型可能用公开 API "填坑"

#### 与稀疏注意力的叠加效应

风险二（稀疏注意力丢弃 system prompt）和风险三（Engram 固化过时知识）会 **叠加**:

1. System prompt 中的 "本项目使用 PyTorch 2.19" 指令被稀疏注意力丢弃
2. Engram 高置信地注入 PyTorch 2.9 的 API 模式
3. 模型既不知道应该用新版本（指令被丢弃），又极其确信自己知道答案（Engram 查表）
4. 结果: **静默地生成使用过时 API 的代码，且无任何不确定性信号**

#### 需要的系统级防御

1. **强制 Context 注入而非依赖 Parametric Knowledge**:
   - System prompt 中明确声明 "使用项目中的 API 版本，不要依赖你的训练知识"
   - 将 `requirements.txt`、`go.mod`、`package.json` 等依赖版本信息注入 context
   - 当模型生成的 API 调用与项目中已有的 import/调用模式不一致时，触发验证

2. **LSP/WebFetch 验证**:
   - 利用已有的 LSP 集成，在生成代码后通过 `hover`/`goToDefinition` 验证 API 签名
   - 对关键 API 调用，使用 WebFetch 抓取最新文档进行交叉验证
   - 将验证结果作为 tool_result 反馈给模型，强制其修正

3. **项目代码模式学习**:
   - 从项目已有代码中提取 API 使用模式（而非依赖模型的 parametric knowledge）
   - 将 "项目中如何调用 xxx 函数" 的示例注入 context
   - 让模型的 attention 机制（而非 Engram lookup）处理这些上下文

4. **版本感知的 Code Generation**:
   - Read tool 读取项目依赖文件后，自动在后续 prompt 中注入版本约束
   - 对 detect 到的 API breaking change（已知的不兼容列表），主动提醒模型

**代码影响**:
- `prompt/system.go`: 根据项目依赖版本，动态注入 "使用 X 版本的 API" 指令
- `tool/`: 编辑/写入代码后，可选的 LSP hover 验证步骤
- `agent/loop.go`: 代码生成后，检测是否与项目已有 import/调用模式冲突
- 新增: 项目代码模式分析（扫描已有代码的 API 使用习惯，注入 context）

### Provider 层重构建议

```
当前: anthropic.go | openai.go
建议: anthropic.go | openai.go (通用) | openai_deepseek.go | openai_glm.go | openai_qwen.go
      ↓              ↓                  ↓                    ↓                ↓
   Anthropic     OpenAI 通用       DeepSeek 特化        GLM 特化          Qwen 特化
   thinking block  reasoning_*     thinking+tools       thinking mode     thinking mode
   budget_tokens   (基础处理)       交错事件              malformed容错     XML workaround
```

各特化 provider 继承 `openai.go` 的基础逻辑，只覆盖差异部分（thinking 参数、tool call 容错、context window 大小、compact 策略配置）。

## 独创构思

> 原版 Claude Code 是为 Claude 模型设计的单模型 agent。我们在多模型 + 国产模型架构风险的现实中，必须走自己的路。

### 已实现的独创功能

1. **Write/Edit 的 Tab/Space 智能修复** — Go 文件写入时自动 spaces→tabs（原版无此处理）；Edit 的 5 级模糊匹配含 tabs↔spaces 双向归一化（原版只有单向）
2. **默认 Prompt 注入优化** — system prompt 组装时按项目类型精简注入（原版对 Claude 优化，我们需针对不同模型压缩）
3. **多模型 Provider 统一抽象** — 原版只接 Anthropic；我们的 `StreamChat` 返回 `<-chan StreamEvent` 统一 Anthropic/OpenAI 流式响应，一套 agent loop 驱动多模型

### 计划实现 — 应对三大架构风险

4. **Thinking Token Budget** — per-model 配置最大 thinking token 数，超限截断 reasoning 输出。原版 Claude 用 `budget_tokens` 原生控制，国产模型没有这个能力，必须 harness 层实现
5. **Reasoning 去重回放** — 多轮对话中历史 reasoning_content 不原样回放，而是压缩为摘要或直接丢弃。原版 Claude 的 thinking blocks 不计入后续上下文（服务端处理），国产模型的 reasoning_content 客户端必须自行管理
6. **Thinking 模式分级** — "快速模式"（禁用 thinking）vs "深度模式"（启用 thinking），根据任务复杂度自动切换。简单 Read 操作不需要推理，复杂重构才需要深度思考
7. **Tool Schema 按需注入** — 原版 ToolSearchTool 为了省 token 成本；在我们的场景下价值升级为 **可靠性保障**：减少 system prompt 体积以提高稀疏注意力下的指令遵从率。只注入当前任务相关的 3-5 个工具 schema
8. **关键指令锚定** — 在用户消息中重复核心规则（利用 attention sink 特性），对 safety-critical 指令用 `<important>` 标签强化。对抗稀疏注意力丢弃 system prompt
9. **Per-Model Compact 策略** — 大上下文模型（GLM-5 百万级）跳过 compact；小上下文模型（DeepSeek 128K）更积极压缩。原版只有一套 compact 策略
10. **项目代码模式学习** — 扫描项目已有代码的 API 使用习惯，注入 context 让模型的 attention 机制（而非 Engram lookup）处理。对抗 Engram 固化过时 API
11. **版本感知 Code Generation** — Read tool 读取 `go.mod`/`package.json`/`requirements.txt` 后，自动在后续 prompt 中注入版本约束。对抗模型用训练时的旧版 API "填坑"
12. **LSP 验证循环** — 生成代码后可选的 LSP hover/goToDefinition 验证 API 签名，不一致时作为 tool_result 反馈强制修正。原版没有这个闭环

### 计划实现 — 新 Feature

13. **实时视觉流辅助前端开发** — 屏幕录制/共享组件捕获开发者操作 UI 的过程（hover 状态、动画时序、布局响应），通过视频帧流发送给支持视频理解的模型（Qwen3.5-Omni TMRoPE）。不是静态截图，而是连续帧序列 + 时间戳，模型能理解 "这个过渡不自然"、"下拉菜单被遮挡了" 等动态问题
14. **Provider 特化层** — 基础 openai.go + 各厂商特化子类（DeepSeek thinking+tools 交错事件、GLM 流式 tool_call malformed 容错、Qwen XML workaround）。原版只有 Anthropic 一家，不需要这层

## 代码风格

- Go 标准格式 (gofmt)，tab 缩进
- 中文注释说明对标 Claude Code 的哪个模块
- 包名用单数 (tool, provider, message)
- 错误处理返回 `(*Result, error)` 给 tool，agent loop 统一处理
- 新功能先在 `internal/` 下独立包实现，通过 interface 接入 agent loop
