# 任务待办池（Backlog）

> **这是需求方的唯一入口。** 你只需往对应优先级的表里加一行，AI 会按序消化。
> 不需要关心当前开发到哪了，不需要等 AI 空闲，随时追加即可。

**配套文件：** 开发计划 `dev-plan.md`（AI 维护的排期与依赖分析） · 进度流水 `dev-progress.md`（已完成任务的归档）
**驱动方式：** `/task-add <需求描述>` 录入 · `/task-next` 让 AI 消化下一个 · `/task-status` 看全局

---

## 状态机

```
待开始 ──► 进行中 ──► 待验证 ──► 已完成
   │          │          │
   └──► 已取消 └──► 阻塞  └──► 打回（验证不通过，回到进行中）
```

| 状态 | 含义 | 谁改 |
|------|------|------|
| `待开始` | 已录入，排队中 | 需求方录入时 / AI |
| `进行中` | AI 正在实施（同一时刻最多 1 个） | AI |
| `阻塞` | 缺前置条件（依赖任务未完成 / 需人工操作 / 需你决策） | AI |
| `待决策` | 自动循环遇到需你拍板的问题，已挂到「待决策」表，答复后回到待开始 | AI |
| `待验证` | 代码已提交并过门禁，等你实际使用确认 | AI |
| `已完成` | 你确认无误，移入 `dev-progress.md` | 需求方确认后由 AI 移动 |
| `打回` | 你使用后发现不对，说明问题后回到进行中 | 需求方 |
| `已取消` | 不做了 | 需求方 |

## 优先级定义

| 级别 | 含义 | 消化顺序 |
|------|------|---------|
| **P0** | 线上故障、数据错误、安全漏洞 —— 打断一切 | 立即，可中断进行中的任务 |
| **P1** | 用户可感知的功能缺失或体验问题 | P0 清空后 |
| **P2** | 功能增强、重构、技术债 | P1 清空后 |
| **P3** | 想法池，做不做都行 | 有余力时 |

---

## P0 — 紧急（打断一切）

| ID | 任务 | 模块 | 状态 | 依赖 | 备注 |
|----|------|------|------|------|------|
| — | 暂无 | | | | |

## P1 — 重要

| ID | 任务 | 模块 | 状态 | 依赖 | 备注 |
|----|------|------|------|------|------|
| T-001 | `--bind=user` 下 search / vision MCP 内部再发给模型的请求不带用户头，会从整个账号池取账号；须把调用方的用户带到内部请求上，按该用户的账号选取 | gateway（search/vision MCP、bind.go、user_accounts.go） | 待验证 | — | 审查发现；同类修复见 03352af6。自动交付（2026-10-29）：commits e2d4774e 82e54e50 3caa0e65；覆盖 search/vision MCP、请求内 web_search、Codex web.run（含 ChatGPT 后端）、Kimi Code 搜索、文本请求里的图片描述；验收：调度员重跑 `go test -count=1 -timeout 45m ./internal/gateway` PASS（452.8s），测试未改弱（新增 11 个测试函数），code-reviewer + security-analyst 发现已修；假设：用户自己的账号都答不了时返回原因、不借别人账号；Codex web.run 无可用 ChatGPT 账号时退回 magpie 自己搜（同样按用户绑定）；group / X-Magpie-Account 请求本身不绑定，但其内部搜索和描述会绑定用户。未用真实 ChatGPT / Kimi 账号跑过。验证：magpie serve --bind=user，同一 provider 两个账号，X-Magpie-User: alice / bob 分别调 web-search MCP、vision MCP、Codex web.run，路由页确认各用自己的账号 |
| T-003 | `--bind=user` 下画图和其他 /v1 路由不按用户绑定选账号：/v1/images、/v1/videos、image MCP 的 generate_image/generate_video、/v1/embeddings、/v1/rerank、/v1/audio/*、/v1/systemone 要求有用户，却会用到绑给别的用户的账号 | gateway（draw.go:854 drawOnAccounts、image_mcp.go:217、gateway.go） | 待验证 | — | 验收：每条路由一个测试，用户 A 不会用到用户 B 的账号，去掉修复时变红。来源：衍生自 T-001（第 1 层，security-analyst MAJOR-1）；衍生层级：1；建议把 tierOf + bindUser 收窄做成共用函数（可复用 usersOwn）；并入（衍生自 T-002 review Medium）：带用户的 count_tokens 仍从整个账号池 s.candidates 选账号（gateway.go countOn 约 1505-1530），不按用户收窄、不看 AccountHeader，会占别人账号的 MaxRPM、429 时让该账号对所有用户休息；自动交付（2026-10-29）：commits 623f263c 1f8f6c52。--bind=user 下画图（含编辑、image MCP）、视频（含 MCP、远端 magpie）、embeddings、rerank、音频、System One、带用户的 count_tokens 只用该用户绑定的账号，复用 bindUser/growUser（bind.go ownOf/usersOn）；视频按启动时的 key 轮询、只有本人能取（别人 404）；401/403 补 spare 或返回遮掩的 429；count_tokens 只读已有绑定，否则本地估算。验收：调度员 verify_tests.sh 基线/交付后 `go test -count=1 -timeout 45m ./internal/gateway/` PASS（467s），测试未改弱（bind_test 一处因 count 只读绑定补了 writeUsers，断言未放宽）；code-reviewer + security-analyst 两轮，High/Medium 全修，终审无 High/Medium。假设：group 请求不按用户收窄；用户未绑定且 provider 全不可用时非 turn 路由返回 503（带 Retry-After）；视频 id 的用户标记 = key+用户名哈希，挡不住持 key 者冒用用户名（X-Magpie-User 自报）；旧视频（无用户标记）在 --bind=user 下 404；未在真实厂商上跑。验证：`go test -count=1 -run 'TestUser|TestSessionRoutes|TestKeepIn' ./internal/gateway/`；或 --bind=user 下两个用户各调两次 /v1/embeddings 应各自固定不同 key，B 去 GET A 的视频 id 应 404 |
| T-007 | 账号登录日志补全：登录开始 / 成功 / 失败各记一行（provider、账号、会话 ID、原因、HTTP 状态）；每种账号进入 / 退出「需重新登录」都记；刷新失败都记；日志不得含 token / code / 登录 URL | provider（signin.go、各 *_signin.go、pluginsignin.go、logins_on.go、chatgpt_api.go、google.go）+ gateway（signin_revoked.go） | 待开始 | — | 验收：每条登录路径（内置 OAuth / 设备码 / 浏览器、Claude Code CLI、插件、插件 API Key、粘贴回调）开始、成功、失败各一行，字段统一；lapse 进出（Claude、revoked 401、Codex、ChatGPT API、Copilot、插件）都记；修掉 signin.go:681 原样记 Claude CLI 最后一行的泄露，测试断言日志无 code= / token；Claude 与 Copilot 不再重复两行。注意 T-006 已加了 lapse 日志（noteSeatLapsed / noteTryLapsed），统一到同一格式即可。来源：需求方投递（2026-10-29）|
| T-008 | GUI 登录提示：登录进行中 / 成功 / 失败（可读原因）在页面上提示；账号「需重新登录」时在账号行和 Providers 页明确提示并给重新登录入口；同步启动失败也有提示 | gui（assets/app.js、providers、i18n） | 待开始 | — | 验收：各类登录（内置 OAuth、设备码、Claude Code CLI、插件、插件 API Key）都能看到进行中 / 成功 / 失败提示，失败原因可读且不含 token / code；两个引擎、窄宽度、所有语言（含 i18n-keys），布局改动跑全量 GUI 套件。需求方决定（2026-10-29）：T-006 的 routing-lapsed.test.cjs 在本任务一起跑（需先装 Playwright 1.62.x，装依赖前问需求方）。来源：需求方投递（2026-10-29）|
| T-009 | `--bind=user` 下用 X-Magpie-Account 指定一个健康账号会跳过用户绑定，可能用到绑给别的用户的账号 | gateway（gateway.go pin 路径 / pinTo） | 待开始 | — | 验收：--bind=user 下 pin 到别的用户的 seat 被拒或回到自己的绑定，测试去掉修复变红；先确认是否有意设计。来源：衍生自 T-006（第 1 层，security-analyst INFO-2，开工前就存在）；衍生层级：1；并入（衍生自 T-003）：count_tokens（countOn）带 X-Magpie-Account 时没校验 pin 是否属于该用户 |
| T-011 | 非 --bind=user 模式下也不把「需重新登录」原文发给调用方：没有其他可用账号时、X-Magpie-Account 指定的账号需重新登录时，改为 429（带 Retry-After）稍后重试；提示只进 GUI 账号状态和日志 | gateway（gateway.go lapsedBack / pinnedLapsed / failAuth 回传路径） | 进行中 | — | 验收：非绑定模式下上述两种情况响应体和响应头不含 sign in / sign-in / expired / revoked / authenticate 和账号名（可复用 limitedNotLapsed），/api/providers 账号行与日志能看到 lapse；--bind=user 下行为不变；测试去掉修复变红。来源：需求方决策（2026-10-29，T-006 第 3 点选 B）|
| T-006 | 账号需要重新登录后已不可用，但请求仍会路由到它，报「Claude Code: Claude Code could not authenticate this account; sign in again in magpie」；需重新登录的账号应离开路由，重新登录成功后再回来 | gateway（候选选择 / bindUser / 内部 search·vision）+ provider（lapsed / revoked 状态） | 待验证 | — | 验收：账号进入「需重新登录」后新请求不再选到它，改走同 provider 其他可用账号；无其他可用账号时返回明确的「需重新登录」错误而不是一直打到该账号；重新登录成功后自动回到路由；覆盖所有选账号路径（serve candidates、bindUser、内部 search/vision 等）及各类 lapse（Claude、Codex、ChatGPT API、Copilot、插件、gateway 401 revoked）；测试去掉修复时变红。来源：需求方投递（2026-10-29）；优先级 AI 判定，可改；自动交付（2026-10-29）：commits 42478b89 e38a2470 3cb03ada df779d95 e90441cb 966144c7 70945cfd 72d4c220。需重新登录的账号在同 provider 有别的可用账号时离开路由，重新登录 / 后台刷新成功后自动回来；--bind=user 下保留原绑定，用户账号都不可用且有名额时当场补 spare（200），否则 429（Retry-After + 限额头，不含任何登录字样）；绑定文件读不出 503；X-Magpie-Account 指定的账号 lapsed 时按需求方 a/b/c 处理。验收：调度员在 72d4c220 重跑 `go test -count=1 -timeout 45m ./internal/provider/ ./internal/gateway/` 全过（provider 84s、gateway 463s），测试未改弱（旧测试改动均为需求方决定所致并已说明）；code-reviewer / security-analyst 共 4 轮，High/Medium 全修，剩 1 个可选 Low（非 last failAuth 每次读一次 logins.json，锁外）。⚠ 高风险区域改动：internal/provider/claude_auth.go。GUI 测试 routing-lapsed.test.cjs 按需求方决定延到 T-008（本机无 playwright）。全量 ./... 有 5 个既有红（开工前同样红，见 T-010）。假设：spare 在原账号恢复后保留到 sweepIdle（3 天）；短暂休息不触发 spare；Retry-After 默认 5 分钟；429 规则只在 --bind=user 且带用户头时生效，非绑定模式仍返回带原因的 403；Stays: Strict 会话会被移到别的账号（trace 记录）；需求方决策（2026-10-29）：①临时 spare 维持现状，sweepIdle 3 天不活跃才释放；②侧面痕迹（用量明细原因文字、固定 Retry-After、逐个 pin 可枚举）——自用服务，不改；③「不把需重新登录告诉调用方」B 所有情况都生效 → 需改代码，另开 T-011，本任务不重开。验证：--bind=user 下让某用户绑定的账号需重新登录，请求应落到补的 spare（200）或返回 429 且无登录字样；重新登录后回到原账号 |

## P2 — 增强 / 技术债

| ID | 任务 | 模块 | 状态 | 依赖 | 备注 |
|----|------|------|------|------|------|
| T-002 | `POST /v1/messages/count_tokens`（及无 /v1 前缀的同名路由）未经 `userNeeded` 检查，与 /v1/messages 不一致 | gateway（gateway.go 路由注册） | 待验证 | — | 需求方决策（2026-10-29）：选 A——不带用户时不碰任何账号、用本地估算回答、不返回 400；带用户时不变；Anthropic 与 Gemini :countTokens 一起改，各补「无用户时 upstream 未被请求」测试，既有断言不放宽，文档 gateway-routing.md:174 的理由改成与代码一致。审查发现；不消耗用量，影响小。自动循环（2026-10-29）未改代码：count_tokens 免用户是既有设计（bind_test.go:228-236 TestUserBindingNeedsTheUser 断言、gateway-routing.md:174 有写），但理由「计数不选账号」与代码不符——countOn 经 s.candidates 转发到任意账号、不按用户收窄。Gemini :countTokens 同类须一起处理；无 /v1 前缀的同名路由不存在；自动交付（2026-10-29）：commits 118cc2b9 bbc8b3b7；--bind=user 下无用户 / 用户头格式错 / tier 不合法的 count_tokens 本地估算返回 200、上游 0 请求，带用户或按会话绑定不变，Gemini :countTokens 原本就本地估算只补测试；验收：调度员重跑 `go test -count=1 -timeout 45m ./internal/gateway/` PASS（452.2s），测试未改弱，两份审查无 Critical/High、Low 已修；假设：user-tiers.json 读不出时不当作无用户（与 noUser 一致）；未在真实厂商上跑过。验证：`go test -run TestUserBindingCountsWithoutTheUserLocally ./internal/gateway/`，或 --bind=user 下不带用户头调 /v1/messages/count_tokens，应返回估算且路由页无该次请求 |
| T-010 | 修复 5 个既有红测试：TestProxyCheckSaysAProxyPassedBy（gui）、TestGitSignInRefusedSaysSo（library）、TestHostToldTheGatewayAddress / TestPluginSOCKSProxy（plugin）、TestCombinedOutputOwnAnswersForItself（proc） | gui / library / plugin / proc | 待开始 | — | 验收：在 origin/main 最新上确认是否同样红；红就按 LESSONS 找原因修，不重跑碰运气；是本机环境问题就写清原因。来源：衍生自 T-006（第 1 层，全量运行发现，066b11f9 上同样红）；衍生层级：1 |
| T-004 | `--bind=user` 下路由分类器 / 决策模型的 askChat 用 context.Background() 发出，不带用户和 gateway key，会从整个账号池取账号，也绕过调用方 key 的限制 | gateway（classify.go:471） | 待开始 | — | 验收：传入外层 ctx 配合 forUser / askAsUser，测试证明分类请求落在调用方用户的账号上。来源：衍生自 T-001（第 1 层，code-reviewer M2 / security-analyst MINOR-3）；衍生层级：1；并入（衍生自 T-003）：视频 startVideo 与 System One serveDecideTries 的重试里，用户自己的账号 failAuth 时没有补 spare、厂商 401 原文可能透传，按 turn 的 userTry 处理；注意：worker-T-003 在 T-003 验收后又提交了 775de9ec（System One 决策在用户被撤销的账号上补 spare 或返回限额，不透传厂商原文），未经调度员验收和审查，本任务开工时须先验证并审查它 |
| T-005 | 加固：`--bind=user` 下 serve 对没有用户、又不带「magpie 自身请求」标记的请求一律拒绝 | gateway（gateway.go:2359） | 待开始 | T-003, T-004 | 验收：守卫测试覆盖进程内所有 s.serve 调用方。来源：衍生自 T-001（第 1 层，security-analyst MINOR-4）；衍生层级：1；依赖 T-003/T-004 先让各内部调用方都带上用户 |

## P3 — 想法池

| ID | 任务 | 模块 | 状态 | 依赖 | 备注 |
|----|------|------|------|------|------|
| — | 暂无 | | | | |

---

## 待决策

> 自动循环遇到需要你拍板的问题时挂在这里，然后跳去做别的任务。回答方式：在调度会话里直接说「T-0NN 选 A」，或终端 `qtask -d T-0NN "选 A，理由…"`。你答复后任务自动回到排队。

| 任务 | 问题 | 选项（推荐在前） | 挂起时间 |
|------|------|-----------------|---------|
| — | 暂无 | | |

---

## 人工阻塞事项

> 需要你（人）动手才能推进的，AI 无法代劳。这里的条目会阻塞对应任务。

| 编号 | 描述 | 阻塞的任务 | 状态 |
|------|------|-----------|------|
| — | 暂无 | | |

---

## ID 分配规则

- 格式 `T-NNN`，三位数字，全局递增，**永不复用**（即使任务取消）
- 下一个可用 ID：**T-012**（AI 录入后须更新此行）
- 人工阻塞事项用 `H-NNN`，下一个可用：**H-001**
