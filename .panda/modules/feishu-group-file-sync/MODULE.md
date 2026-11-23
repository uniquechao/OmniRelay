# Feishu Group File Sync

## 职责边界

从已归档、已绑定项目的飞书群消息发现文件资源，通过网盘和项目入口提供有来源授权的内容。群历史覆盖范围由聊天同步服务维护；本模块复用其消息、群绑定和成员检查。

## 关联源码路径

- `internal/drive/` 管理来源索引、Wiki 映射、对象存储、版本、预览、访问校验和人工归属。飞书协议适配位于 `internal/feishu/files.go`，工作台装配位于 `cmd/omnirelay/server.go`。
- `internal/project/panels.go` 与 `web/drive*.js`、`web/projects.js` 消费同一文件、版本和来源。数据库兼容升级见 `migrations/0023_group_file_sync.sql`。

## 长期设计约束

- 有效归属取当前可读来源群的项目与读者可见的人工关联之并集。群改绑立即影响历史来源，人工标签本身不能授予内容访问权。旧归属原始记录在 `feishu_file_project_legacy` 中留存。
- 逻辑文件、消息来源、来源可读版本和物理内容分别标识。租户内相同 SHA-256 可复用一个对象；版本访问必须命中当前可读消息在 `feishu_file_share_versions` 中的记录。
- 新原件及文档正文通过私有 S3 存储，旧分块仍可读取。对象上传与数据库完成状态分别提交，领取时间和凭据代次防止过期任务发布。已发布对象与未发布上传分别追踪，后者可在宽限期后回收。

- Docx 图文采集与预览复用同一文件版本和来源授权。块分页读取最新修订，采集前后核对修订；图片保存进快照。显式历史修订读取可能被飞书拒绝，不可据此误判当前文档无权限；图片失败或修订变化不发布新版本。PNG/JPEG/GIF/WebP 均需注册尺寸解码器；接入新格式时同时核对采集端尺寸校验和浏览器展示，避免只在前端声称支持而整篇无法同步。预览只渲染规范化块和允许的链接，通过 canvas 展示已授权图片，不放开 CSP 或暴露平台临时地址。

- Docx 人员引用显式按 open_id 读取；先从同应用当前目录和账号解析姓名，缺失时仅对文档引用的人员用原 Bot 补查通讯录。快照保存姓名与内部人员键，不暴露原始 open_id；无权解析时明确标示未解析用户。关联文档保留标题及安全链接，提醒保留带时区的日期。转换逻辑更新后由正常后台重读生成新版本，不覆写历史快照。

## 接口与测试

- `/api/v1/feishu/files` 提供列表、来源、版本、预览、归属和原件读取。下载和分段播放均经服务端授权；项目来源可附带 `source_project` 限定入口。Docx 新版本保存结构化图文快照，旧纯文本版本保持可读。
- `GET /api/v1/feishu/files/sync` 返回当前用户可读群的同步快照，按消息来源统计文件进度。同一文件可有多条来源。网盘每次打开或刷新页面时独立读取一次快照，分类和分页只更新文件列表。
- 定向用例见 `internal/drive/group_sync_test.go`、`internal/drive/drive_test.go`、`internal/feishu/files_test.go`、`migrations/group_files_test.go` 及 `tests/integration/chat_reader_browser.test.mjs`。数据库回归需要隔离的 `OMNIRELAY_TEST_DATABASE_URL`；在专用的测试桶配置下设置 `OMNIRELAY_TEST_S3=1` 可验证真实对象读写和回收。

<!-- panda:module-meta:start -->
---
sync_uid: 01a1f9e2-dc3b-7bfb-8296-308593019611
module_id: 79
project_id: 25
slug: feishu-group-file-sync
display_name: "Feishu Group File Sync"
agent: codex
source: auto
status: active
created_ts: 1795193756731
last_used_ts: 1795193756854
---
<!-- panda:module-meta:end -->
