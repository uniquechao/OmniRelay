# #20261117-1cdd62a 接入飞书 SDK 长连接、Kubernetes Lease 与可信消息过滤

## 原始需求

依据：design/specs/omni-relay-feishu-integration.md。必读：design/engineering/feishu-integration-standards.md 的通用标准、后端工程师和完成定义。
负责角色：后端工程师。
范围：设计 §3 Go 到飞书；internal/feishu、cmd/omnirelay、config/backend.env.example、deploy/k8s 的 Bot Lease/RBAC 最小资源。
实施：固定官方 Go SDK 版本，装配可取消连接生命周期、退避重连与有效心跳；持有者协调最新配置，PostgreSQL 代次保护最终写入。将可信私聊/群 @ 映射到 issue 3 受理接口，完整标注需要真实 SDK 实测的行为。
交付：实现/样例、相应测试、必要文档和脱敏验证结果；当前缺少模块索引，不自行创建模块或 issue 过程页。

<!-- pandados:acceptance -->
- [ ] 双实例只由 Lease 持有者开启连接；失去 Lease、开关关闭或凭据换代会停收并拒绝旧代次提交；滚动交接、重启及取消退出没有遗留连接或 goroutine。
- [ ] 持久记录 disabled/connecting/connected/reconnecting/unavailable 及配置版本/代次；过期心跳返回 unavailable，旧实例不能覆盖新代次在线状态。
- [ ] 仅受理 im.message.receive_v1 中来源应用/组织匹配的人类私聊及明确 @ 当前 Bot open_id 的群消息；过滤其他 Bot、无关 @ 和错误来源；保留原话题与回复消息 ID。
- [ ] 回调只校验与持久化，提交后确认；自动化覆盖重复消息、数据库失败和晚到回调，记录 3 秒限制内的处理证据。
- [ ] SDK 启停、在线信号、处理超时/重投及断线重连有版本化验证步骤；最小 Lease/RBAC 配置可复现，真实群和私聊最终由 issue 10 验收，不能用 mock 声称真机通过。

<!-- panda:issue-meta:start -->
---
sync_uid: 01a1ec81-d54e-7dc4-8360-a91590a8ce01
issue_id: 20261117-1cdd62a
module_uid: null
module: unclassified
agent: codex
category: task
impl_mode: seq
status: done
created_ts: 1794969294158
---
<!-- panda:issue-meta:end -->
