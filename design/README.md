# OmniRelay 原型与设计

原型与设计规范统一在本目录更新。HTML、样式、脚本、图片和第三方依赖随 `prototype/` 一并维护，目录整体迁移时保留相对位置。

## 交互原型

- [工作台](prototype/omni-relay-workbench.html)
- [项目工作台演示](prototype/omni-relay-workbench.html#/projects/beichen/overview)
- [飞书 · 机器人记录](prototype/omni-relay-workbench.html#/feishu/bot-chats)
- [飞书 · 文件网盘](prototype/omni-relay-workbench.html#/feishu/drive)
- [登录演示](prototype/omni-relay-workbench.html#/login)
- [飞书 Bot 交互与卡片](prototype/omni-relay-feishu-prototype.html)
- [色彩系统](prototype/omni-relay-color-system.html)
- [加载状态组件演示](prototype/omni-relay-workbench.html#/developer/components)
- [UI 规范](specs/omni-relay-ui-standards.md)

工作台、登录和卡片使用演示数据。双击 HTML 可在浏览器本地打开。登录页提供账号密码和飞书授权演示。构建正式工作台时，源码放在工程根目录的 `frontend/src/`，按本目录最新确认的原型验收视觉、交互和功能。

登录和品牌动效基准：2026-11-25 会话「OmniRelay-设计」最后确认的「定场切页」版本。三组文案每 8 秒轮换，Logo 使用共享「循光成环」矢量，背景为椭圆线圈、紫青光晕与流星；替代旧四圈波纹和二进制信息流版本。正式入口使用 `frontend/src/features/workbench/Login.tsx`，品牌组件为 `frontend/src/ui/components/BrandMotion.tsx`，矢量来源统一为 `frontend/src/ui/loading.ts`。正式认证接现有同源接口，演示授权和账号不会打包进入正式页面。

## 系统设计

- [本轮需求改动与开发交接](specs/omni-relay-development-handoff-2026-11-19.md)
- [本轮开发任务分解指令](engineering/development-instructions-2026-11-19.md)
- [全系统 Loading 开发指令](engineering/loading-development-instructions-2026-11-19.md)
- [系统模块与服务边界](specs/omni-relay-module-design.md)
- [Go 主服务详细设计](specs/omni-relay-go-service-design.md)
- [项目模块需求（评审稿）](specs/omni-relay-project-requirements.md)
- [飞书 Bot 配置、登录与通讯录同步](specs/omni-relay-feishu-integration.md)
- [Kubernetes 部署设计](specs/omni-relay-k8s-deployment-design.md)

## 品牌素材

- [双色横版 Logo](prototype/omni-relay-logo-bicolor.png)
- [飞书 Bot 头像](prototype/omni-relay-avatar.png)
- [CSS 色彩变量](prototype/omni-relay-colors.css)

OmniRelay 的当前选型与实施边界见 `specs/`。设计参考与原型引入的开源项目见 [开源项目与致谢](../README.md#开源项目与致谢)。
