# 飞书接入目标契约与样例

依据：[接入设计 §2–5](../specs/omni-relay-feishu-integration.md)、[工程标准](feishu-integration-standards.md)。本条负责固定供前后端复用的目标协议，不包含 Bot 领域、OAuth 修补或目录改造。下文标记“实施约定”的内容用于补齐设计留白；其余保留设计规定值。

Bot 管理接口已接入 Go 服务。OAuth 错误仍统一回 `error=feishu`，目录响应字段按 `internal/directory/http.go` 固定。

## 文件与运行方式

在项目根执行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/contract -p 'test_*.py' -v`，只需 Python 3 标准库，不联网、不用数据库、不跳过测试。

| 文件（均位于 `tests/fixtures/feishu/`） | 用途 |
| --- | --- |
| `bot-config.json` | GET/PATCH 成功完整响应；初始 0/关闭、5 种连接状态、ETag、无收信时间 |
| `bot-checks.json` | GET 检测任务；4 种任务状态、3 种检测项状态、4 种脱敏错误 |
| `check-submissions.json` | POST 新任务、同键重试、异键合并、终态重试 |
| `idempotency-sequences.json` | 活动任务、终态、保留期限与凭据换代的连续场景 |
| `http-matrix.json` | 逐行独立的 HTTP 正反例，初始配置版本固定为 0、目录版本为 17 |
| `oauth.json` | 3 种新错误、旧 feishu 兼容及 state 拒绝场景 |
| `directory.json` | 部门/人员/历史列表的同版快照、初次无快照、失败保留与撤权隐藏 |
| `directory-submissions.json` | 扁平同步任务、coalesced 正反例及全部运行状态 |

JSON 内的 `name` 是样例标签。`status/headers/body` 是 HTTP 外层封套，不属于响应正文。目录组合样例的 `departments/people/runs` 分别是三个 GET 正文；不是一个新接口。所有标识均为虚构值；权限、会话、CSRF 在矩阵中用状态标签表达，真实适配器须以隔离测试账号生成，禁止把标签当作实际头值。

## 读取、修改与连接状态

- GET `/api/v1/system/feishu-bot`：`system.read`，返回 200 和强 ETag `"bot-<version>"`。只读存储，轮询不访问飞书。未初始化 `version=0`、`enabled=false`。
- PATCH 同地址：`system.configure`，仅接受 `{"enabled":boolean}`，成功 200，正文同 GET；每次重新鉴权并校验同源 Origin 与 CSRF。开关改变、版本递增与审计在同一事务；相同值成功但版本和变更审计保持不变。
- `If-Match` 缺失 428；过期 412。实施约定：仅接受与当前版本完全相同的一个强 ETag，弱 ETag、`*`、多值或格式异常均 412，不能绕过版本控制。缺少 enabled、非布尔或未知字段 400。
- 连接枚举固定 `disabled/connecting/connected/reconnecting/unavailable`。响应不新增连接版本字段；底层必须按配置版本、连接代次及当前持有者校验状态有效性。
- 实施约定：持有者每 10s 写心跳，`OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL=30s`；以数据库时间计算 `now-last_heartbeat_at >= TTL` 即过期。启用状态下心跳为空不能视为在线；关闭优先返回 disabled。30s 边界、旧持有者和未来时间异常均需后端测试，不使用客户端时钟。
- `last_message_at=null` 表示本次连接尚无成功持久化的合规真实事件；检测通过不能设置它。新的连接代次须清空上代收信时间。全部非空时间采用 UTC RFC 3339（以 Z 结尾）。
- `credentials_configured` 表示部署是否注入凭据，不代表凭据有效。`bot_name` 未知时为空字符串，不伪造名称。

## 检测任务与幂等

POST `/api/v1/system/feishu-bot/checks` 使用 `system.read`，仍须 Origin 和 CSRF，正文必须是空对象。202 正文固定 `id/state/reused`；GET `/checks/{id}` 返回 200 和完整任务。越界应用/组织及不存在任务统一 404；读取者须重新通过 `system.read`，不能仅凭知道 ID 查询。

实施约定：任务 ID 是不透明非空字符串，客户端不得推导。完整任务固定字段：`id`、`state`、`credentials_generation`（十进制正整数字符串）、`created_at`、`started_at`、`finished_at`、`results`。queued 的开始/结束为 null，running 的结束为 null，终态开始/结束均非空。results 是按 `tenant_token`、`bot_info` 排列的数组；queued 为空，running 可包含已完成项，终态包含两项。项固定 `name/state/checked_at`，失败项另有 `error`，通过及跳过项不得包含 error。checked_at 为该项完成/决定跳过的时间。

状态为 `queued/running/succeeded/failed`；项状态为 `passed/failed/skipped`。租户令牌检测失败时 Bot 信息跳过；前项通过后 Bot 信息失败则整任务 failed。错误只允许 `credentials_missing/credentials_rejected/bot_unavailable/provider_temporarily_unavailable`，不能带原始响应、请求头或平台文案。旧代任务保留历史，永远不能覆盖新代诊断。

以下均为实施约定，作为后端持久化与前端重试的共同边界：

1. `Idempotency-Key` 必填，接受 1–128 字符的 `[A-Za-z0-9._:-]`；缺失、空、重复头、非法字符或过长返回 400 `invalid_idempotency_key`。前端一次点击生成一次键，网络重试沿用，用户再次主动检测生成新键。
2. 键绑定作用域为 `(tenant_key, app_id, key)`，保存绑定时的凭据代次与请求指纹。先鉴权及校验合法请求，再查键绑定，最后查活动任务。键不按账号隔离，同源合并不泄漏其他组织；每次仍要求权限。
3. 同键返回原任务，即使该任务已终态或部署已换代，返回 202、当前任务 state、`reused=true`。此返回是历史查询语义，不代表当前凭据已通过。不同键在相同 `(tenant_key, app_id, credentials_generation)` 有 queued/running 任务时绑定同一任务并返回 reused=true。新建任务才 reused=false。
4. 活动任务及所有键绑定不能因时间经过被清除。终态后至少保留 24h，固定保留到 `finished_at+24h`；重试不延长窗口。达到边界可以清理绑定，任务历史保留不得短于所有引用它的绑定。清理后原键视作新键，重新按当前代次合并/新建，前端不得依赖无限重试。
5. `OMNIRELAY_FEISHU_CREDENTIALS_GENERATION=1` 是非敏感、单调递增的部署版本字符串，轮换 App Secret 时所有副本原子更新并递增，不使用秘密值或其哈希。新键仅合并当前代次活动任务；旧键仍回原 ID。读取、领取、续租和提交必须核对代次，禁止旧代次任务更新当前状态。旧活动任务由协调/租约恢复终结为 failed，失败码为 provider_temporarily_unavailable；不新增 cancelled 检测状态。
6. 同键不同指纹返回 409 `idempotency_conflict`，不得覆盖绑定。当前合法正文只有 `{}`，非法正文先返回 400，因此该冲突主要是后续协议扩展的防护，不将非法正文改报 409。并发键绑定与活动任务唯一性依靠数据库事务，不能只用内存缓存。

部署样例在 `config/backend.env.example`。服务启动前，以独立迁移角色运行 `omnirelay migrate` 应用 `0005_feishu_bot.sql`。App ID 和租户标识用于隔离数据，必须配置。缺少 App Secret 时，已有本地账号仍可管理 Bot，检测返回 `credentials_missing`，飞书登录和目录同步需要有效凭据。配置变更写入 `feishu_bot.configure` 审计事件，包含操作者、应用资源和变更版本。

数据库拒绝旧凭据代次重新启用。检测任务的领取租约为 30 秒，Worker 完成令牌阶段后续租。异常退出后，同一代次的实例可在租约过期后重新领取。配置变更及凭据换代会清除当前连接的心跳和收信时间。幂等窗口固定为 24 小时。

## HTTP、OAuth 与目录约束

| 状态 | 验收输入/含义 | 错误正文（实施约定，设计已指定者保持原值） |
| --- | --- | --- |
| 200 | 有权限 GET；管理员合法 PATCH/同值 PATCH | 对应完整正文 |
| 202 | 有权限且通过 Origin/CSRF 的检测或目录同步，含复用 | 对应提交正文 |
| 400 | 未知字段、错误类型、非法检测键、无效目录查询 | invalid_request / invalid_idempotency_key / invalid_directory_query |
| 401 | 无会话、过期或撤销会话 | unauthorized |
| 403 | 权限不足，写操作缺失/跨站 Origin、缺失/错误 CSRF | forbidden |
| 404 | 无此检测任务或跨组织/应用任务 | check_not_found |
| 409 | 快照不匹配；键指纹冲突 | directory_snapshot_changed / idempotency_conflict |
| 412 | If-Match 版本或格式不匹配 | config_version_conflict |
| 428 | 缺少 If-Match | config_version_required |

业务错误正文仅 `{"error":"<code>"}`。检测的平台失败是任务 failed，GET 仍 200；不是凭据错误就返回管理接口 401。GET 不要求 Origin/CSRF，绝不能用写接口的放行条件绕过读权限。矩阵每行单独重置基线，不按文件顺序连续执行。

OAuth：仅浏览器绑定、state 有效且原子消费成功的事务可 303 到 `/#/login?error=cancelled|denied|unavailable`。取消为 cancelled；未开通、停用、异组织为 denied；平台暂不可用为 unavailable。state 无效、过期、重复或浏览器不匹配为 400，不回跳也不建立会话。`error=feishu` 仅作为前端兼容输入样例，不再作为新后端错误目标。成功仍先请求 `/api/v1/me` 确认会话，合法内部 return_to 才能恢复；无权路由须重新核验。固定失败目标不拼接用户提供的外部 URL。

目录沿用现有扁平结构，`snapshot_version` 为正整数或 null，`visible` 为布尔。不可见时版本和 scope 为 null，部门和人员 items 必须为空，历史任务及最近成功时间可保留。people 按 open_id 去重，departments 为全部可见路径。directory_status 为 `unknown/active/inactive/resigned`，account_status 为 `unprovisioned/active/inactive`，未开通 account_id 为 null，目录状态不会自动授予本地账号权限。

部门、人员和同步历史使用同一 snapshot_version；翻页携带，409 后丢弃混版结果重载。sync-runs POST 是扁平 run 加 coalesced，非嵌套 task；ID 为数字。初次无快照和撤权隐藏都 visible=false，靠 last_success_at 与 latest_attempt 区分。临时失败允许 visible=true 保留合法旧快照；已确认范围变化须先隐藏，后续完整发布才恢复。failure_reason 是现有接口的脱敏显示字符串，本条不引入新目录失败枚举。

## 验证边界

离线样例校验严格字段集合、类型、枚举、时间、ETag、生命周期、目录可见性、同版与去重。设置 `OMNIRELAY_TEST_DATABASE_URL` 指向隔离 PostgreSQL 后，执行 `go test -v ./cmd/omnirelay ./internal/feishu ./migrations` 验证 Bot 管理 HTTP、适配器、任务并发与迁移。飞书适配测试为真实请求构造与响应解析注入测试传输层。预发布飞书应用、浏览器和 SDK 验收见[跨角色验收与预发布清单](feishu-integration-acceptance.md)。
