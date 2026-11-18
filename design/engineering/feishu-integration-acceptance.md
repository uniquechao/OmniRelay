# 飞书跨角色验收矩阵与预发布准备

本清单对应 [目标契约](feishu-integration-contract.md) 与[工程标准](feishu-integration-standards.md)。本 issue 完成契约固定与离线样例验证；平台与真实服务验收由实现负责人及最终联调执行，不能以样例通过替代。以下未执行的预发布项统一为“待验证”，不作为契约开发的硬阻塞。

## 角色与接口矩阵

`admin` 对应 system_admin，`operator` 对应 system_operator，`member` 为无附加系统权限的普通成员。HTTP 样例使用这三个简写。目录权限按当前 `internal/access/access.go`：管理员具有 directory.read/sync，运维默认没有；账号有多角色时按实际权限重算。

| 场景 | 管理员 | 运维 | 普通成员 | 无效会话 | 验证责任 |
| --- | --- | --- | --- | --- | --- |
| Bot GET、任务 GET | 200 | 200 | 403 | 401 | Bot 后端＋前端 |
| Bot PATCH | 200 | 403 | 403 | 401 | Bot 后端＋前端 |
| Bot checks POST | 202 | 202 | 403 | 401 | Bot 后端＋前端 |
| directory GET | 200 | 403 | 403 | 401 | 目录后端＋前端 |
| directory sync POST | 202 | 403 | 403 | 401 | 目录后端＋前端 |
| 前端入口和直达路由 | 按 access.admin.feishu_bot 展示并逐请求鉴权 | 可看可测，不能启停 | 无入口，直达显示无权 | 转登录 | 前端 |

所有 POST/PATCH 的同源 Origin、有效 CSRF 为必要条件，正例与缺失/错误各自覆盖。PATCH 必须带当前强 If-Match；检测必须带合法 Idempotency-Key。`tests/fixtures/feishu/http-matrix.json` 为可重放输入，覆盖 200/202/400/401/403/404/409/412/428。真实适配器先为对应账号获取会话与 CSRF，再将 same/cross/absent、valid/invalid/absent 转成实际请求头；同一行同时比较状态、错误正文、ETag 和持久化副作用。请求体的 malformed JSON、重复头、多 ETag 及非法字段也应由后端 HTTP 测试补充，不应由 mock 宣称通过。

## 跨链路与故障验收

| 编号 | 步骤与预期 | 责任与证据 | 当前状态 |
| --- | --- | --- | --- |
| B1 | 空配置读 version=0/关闭；GET 不调用平台；切换保存后 ETag 递增；同值保存不增版本、不增变更审计 | Bot 后端：真实 HTTP、数据库版本与审计计数；前端：编辑/已保存分离 | 待实现后验证 |
| B2 | 两管理员持同 ETag 保存，仅一个成功，另一个 412；缺 ETag 428；刷新保留草稿并由用户再次保存 | Bot 后端＋前端：并发请求与页面状态 | 待验证 |
| B3 | 同键重试同 ID；不同键同代活动任务同 ID；终态同键仍同 ID；24h 边界和活动任务不失效 | Bot 后端：事务/唯一约束/租约测试，重放幂等序列 | 待验证 |
| B4 | 换钥且递增代次：新键不并入旧任务；旧键回历史 ID；旧领取/提交不能覆盖当前状态 | Bot 后端＋连接后端：数据库与脱敏时间线 | 待验证 |
| B5 | 心跳 29.999s 可在线，30s 返回 unavailable；旧持有者无效；无合规事件时 last_message_at=null | 连接后端：可控数据库时间与版本/代次测试 | 待验证 |
| O1 | 已开通成员登录；取消、未开通、停用、异组织及平台暂不可用分别给固定回跳；旧 feishu 前端仍可重试 | OAuth 后端＋前端：实际回跳状态及 `/api/v1/me` | 待验证 |
| O2 | state 无效/过期/重放/错误浏览器 400，无会话；PKCE S256；恶意 return_to 被拒；退出、停用后旧会话失效 | OAuth 后端：数据库测试＋真实浏览器 | 待验证 |
| D1 | 完整分页、多部门同人去重、直接授权人员、未知在职状态；树与列表同版；调岗更新路径；目录新增不自动开通 | 目录后端＋前端：范围摘要、数量及页面 | 待验证 |
| D2 | 初次无快照、运行中旧快照、临时失败保留；缺页/重复页/超时/限流耗尽不发布；409 后清除混版重载 | 目录后端＋前端：故障注入和查询结果 | 待验证 |
| D3 | 撤权/收窄先隐藏旧目录；浏览器清空旧条目与详情；替换完整发布才可见；并发同步同 ID/coalesced=true，手动请求各自审计 | 目录后端＋前端：HTTP、数据库及浏览器 | 待验证 |
| M1 | 首连后在平台保存长连接并发布订阅；私聊与明确 @ Bot 群消息入站；机器人及无关消息过滤 | 连接后端＋验收：专用测试群真实事件 | 待验证 |
| M2 | 同 message_id 重投仅一个入站事件与待路由工作；事务失败不确认；重复先于容量检查；实测回调小于 3 秒 | 连接后端：数据库与耗时，不留消息正文 | 待验证 |
| M3 | 双实例、滚动更新、掉线重连、Lease 丢失及重启恢复；旧代次晚到提交拒绝；无 goroutine 泄漏 | 连接后端＋验收：持有者/代次脱敏时间线 | 待验证 |
| X1 | Bot 关闭后 OAuth 和目录仍成功；同成员 Web 与 Bot 的可信三元组解析同一本地账号 | 全角色联合：同次验收关联结果 | 待验证 |
| X2 | 换钥后分别验证换码、目录读取与 Bot 重连；旧代次在线/收信状态不残留；所有采集无秘密 | 全角色联合：三链路记录 | 待验证 |
| U1 | 桌面/窄屏、浅深色、键盘、未保存离开、空/载入/失败/无权；快速切页/退出忽略晚到结果；检测终态停轮询 | 前端：视口、角色、路由和脱敏截图 | 待验证 |

## 预发布资源清单

| 资源 | 准备要求 | 状态/责任 |
| --- | --- | --- |
| 企业自建应用 | 同组织单应用；机器人及网页能力；记录非敏感 App ID、租户和发布版本 | 待验证／部署负责人 |
| 应用身份及凭据 | 通过部署秘密挂载注入，代次全副本一致；本清单不收集凭据；轮换实验只用预发布 | 待准备／部署负责人 |
| 消息权限 | 设计要求 `im:message.group_at_msg:readonly`、`im:message.p2p_msg:readonly`；订阅 `im.message.receive_v1`，在真实控制台核对名称、审批与发布生效 | 待验证／应用管理员 |
| 通讯录权限 | `contact:contact:readonly_as_app`；核对 scopes、子部门、直属成员与直接授权个人可读；在职状态字段的额外权限须实测后登记，未知字段不能推断为在职 | 待验证／应用管理员＋目录后端 |
| Bot 信息与网页登录 | 核对租户令牌、bot/v3/info、用户换码和身份接口实际所需权限；不凭 mock 断言可用，不添加未经核实的 scope 名称 | 待验证／应用管理员＋后端 |
| 可用范围 | 应用可用范围与通讯录可读范围分开配置；含父子部门、多部门人员、单独授权个人；准备一次收窄/撤销与恢复 | 待准备／应用管理员 |
| 账号 | 已开通启用成员、未开通成员、停用成员、运维、管理员；异组织拒绝用隔离账号验证；不记录个人真实 Open ID | 待准备／测试负责人 |
| 群及客户端 | 同组织专用测试群并加入 Bot；测试成员可群 @ 和私聊；真机浏览器执行登录、取消、退出 | 待准备／测试负责人 |
| HTTPS | 有效证书和完整 `https://<预发布域名>/auth/feishu/callback` 登记；与 PUBLIC_ORIGIN 一致；入口日志/APM 丢弃 callback 查询串；Go 端 HTTPS 与 Secure Cookie 核验 | 待准备／部署负责人 |
| 数据库与双实例 | 专用隔离 PostgreSQL，迁移与 API 分离；OMNIRELAY_TEST_DATABASE_URL 指向隔离库；双实例、Lease 与滚动更新环境 | 待准备／部署负责人 |

权限名称以上述设计为依据，并非本次已实测结论。飞书原始依据链接集中在接入设计 §2–5；真机执行者须按当时控制台和官方文档核对，记录实际审批/范围，无平台权限不虚构成功。

## 官方 Go SDK 待验证步骤

1. 连接后端选择官方 Go SDK 实际版本，在 go.mod/go.sum 锁定版本，记录模块路径、tag/commit、Go 兼容性、许可证。本基线未引入 SDK，本条不随意新增依赖，也不声称已固定经过验证的版本。
2. 在隔离分支/测试环境按该版本官方示例建立首次 WebSocket，再保存平台长连接方式并发布消息订阅；记录建立/失败/取消信号以及 Bot 配置的对应状态。
3. 试验 context 取消、显式关闭、并发关闭及进程退出，确认资源与 goroutine 释放；断网/恢复和平台暂不可用后记录重连时序。核实 SDK 可用在线信号是否足以支持当前持有者心跳，不能把构造成功视为在线。
4. 发送授权测试事件，注入受理前数据库故障、超时、重投；核实回调返回/确认语义、3 秒限制、SDK 超时及平台重投行为。只在事务提交后确认，重复回调返回原受理结果。
5. 双实例切 Lease、停用/启用、凭据换代与滚动更新，延迟旧持有者事件提交，验证数据库拒绝旧代次。SDK 层无法提供的状态必须在适配层明确处理，并同步契约消费者。
6. 用真实私聊与群 @ 更新本连接 last_message_at；无真实事件保留 null。再完成 X1/X2，记录实际 SDK 版本与脱敏证据后才能把这些行标为通过。

## 证据和交付

每项记录：验收编号、代码/部署版本、SDK 版本（若涉及）、UTC 时间、角色、操作、请求 ID、平台请求 ID（若可得）、预期、实际、通过/失败/待验证、证据相对路径。不记录 OAuth code/state 值、PKCE verifier、Cookie、CSRF 实值、App Secret、访问令牌或原始用户消息。截图和导出文件写在项目内并先脱敏。

本条离线证据：运行契约测试输出无跳过；检查范围包括严格结构/枚举/UTC/ETag、HTTP 决策表、幂等保留与换代、目录同版/隐藏及非法样例拒绝。数据库和真实 SDK、浏览器、飞书应用均未执行；最终门禁由系统运行。后续真实验收属于实现与最终联调任务，不能将其待验证状态写成本条样例已验证的能力。

## issue 10 通讯录真机核验

D1–D3 的后端流程已有本地 PostgreSQL 和合成飞书响应的定向证据。以下步骤使用预发布自建应用和专用测试成员执行，实际权限、字段和响应形状仍待核验。

1. 在飞书开发者后台分别核对应用身份 API 权限和通讯录数据范围，发布版本并确认管理员审批生效。用同一应用通过 `POST https://accounts.feishu.cn/oauth/v3/token` 的 `client_credentials` 获取应用身份令牌，取顶层 `access_token` 调用通讯录接口。记录生效权限标识和应用发布版本。 [应用身份令牌](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/access-token/tenant_access_token) · [配置应用数据权限](https://open.feishu.cn/document/api-call-guide/calling-process/configure-app-data-permissions)
2. 在通讯录范围内添加一个部门和一个归属范围外部门的单独授权人员。用 `GET /open-apis/contact/v3/scopes?user_id_type=open_id&department_id_type=open_department_id&page_size=1` 遍历全部页，确认 `department_ids` 返回部门 ID，`user_ids` 返回该应用的 `ou_` ID。逐一检查无额外员工 ID 权限、授予 `contact:user.employee_id:readonly` 两个预发布版本的响应。核验 Open ID 模式下 `user_ids` 是否省略、为空或可读，并记录所需的最小权限组合。另测全员范围对一级部门、根直属人员和 `group_ids` 的实际表现。当前只展开部门和个人授权，非空 `group_ids` 会阻止发布并隐藏旧目录。 [授权范围](https://open.feishu.cn/document/server-docs/contact-v3/scope/list) · [Open ID](https://open.feishu.cn/document/faq/trouble-shooting/how-to-obtain-openid)
3. 对范围内部门用 `GET /departments/{id}/children` 传 `fetch_child=false`，逐页核对 `open_department_id` 和 `parent_department_id`。用 `GET /users/find_by_department` 按 `department_id` 逐页核对 `items[].open_id` 和 `department_ids`。对仅直接授权的人员用 `GET /users/{open_id}` 核对 `data.user.open_id` 与 scopes 中的 ID 一致。两条人员查询均传 `user_id_type=open_id&department_id_type=open_department_id`。分别核对应用身份下 `department_ids` 的省略与空数组行为，以及 `contact:user.department:readonly` 是否影响可见部门归属。 [子部门](https://open.feishu.cn/document/server-docs/contact-v3/department/children) · [直属人员](https://open.feishu.cn/document/server-docs/contact-v3/user/find_by_department) · [单个人员](https://open.feishu.cn/document/server-docs/contact-v3/user/get)
4. 确认 `status` 字段与 `contact:user.employee:readonly` 或应用已有通讯录权限的实际关系。使用激活、冻结、主动退出、已确认离职和状态缺失的测试样本，核对 `is_activated`、`is_frozen`、`is_unjoin`、`is_exited` 和 `is_resigned`。只有明确的 `is_resigned=true` 表示已核实离职。主动退出映射为 `inactive`，没有足够状态字段时为 `unknown`。核对同名、多部门和调岗后的路径，确认其中一个部门撤权后，范围外的路径从目录消失。 [用户资源与状态](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/contact-v3/user/field-overview)
5. 先记录一版可见快照，分别演练同范围临时失败、移除单独授权人员、部门收窄和撤回 API 权限。观察相同范围下旧版本仍返回 200，确认范围变化后旧版本返回 409，完成后置范围复查才恢复可见。对照已开通成员本地账号状态、角色、现有会话和未开通成员，确认目录变化只影响目录显示。记录前后版本、范围计数、字段的存在性和类型、UTC 时间、请求 ID、飞书请求 ID、HTTP 状态及平台错误码。人员与部门使用固定匿名代号，仅保存脱敏结果。
