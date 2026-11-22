# File Netdisk

## 职责边界

飞书文件网盘在 `#/feishu/drive` 展示可访问文件，提供分类、搜索、筛选、分页、来源定位和预览入口。

## 关联源码路径

- `frontend/src/features/workbench/Workbench.tsx` 选择工作台布局，`web/drive.js` 挂载网盘页面和共享列表。
- `design/prototype/omni-relay-drive.css` 由原型与正式网盘共用。

## 长期设计约束

- 网盘使用列表布局。文件行在共享列表的滚动区内滚动，表头吸顶。列表超出可用高度时，分页留在页面底部；内容较少时，分页紧随列表。

## 接口与测试

- `tests/integration/chat_reader_browser.test.mjs` 包含网盘滚动、分页与文件交互的浏览器回归用例。

<!-- panda:module-meta:start -->
---
sync_uid: 01a2032e-4b2d-702f-8b17-632d25d6ca08
module_id: 84
project_id: 25
slug: file-netdisk
display_name: "File Netdisk"
agent: codex
source: auto
status: active
created_ts: 1795349695277
last_used_ts: 1795349695366
---
<!-- panda:module-meta:end -->
