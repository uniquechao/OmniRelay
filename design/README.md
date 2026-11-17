# OmniRelay 原型与设计

原型与设计规范统一在本目录更新。HTML、样式、脚本、图片和第三方依赖随 `prototype/` 一并维护，目录整体迁移时保留相对位置。

## 交互原型

- [工作台](prototype/omni-relay-workbench.html)
- [飞书登录演示](prototype/omni-relay-workbench.html#/login)
- [飞书 Bot 交互与卡片](prototype/omni-relay-feishu-prototype.html)
- [色彩系统](prototype/omni-relay-color-system.html)
- [UI 规范](specs/omni-relay-ui-standards.md)

工作台、登录和卡片使用演示数据。双击 HTML 可在浏览器本地打开。整理时登录动效脚本尚未齐备，登录页使用原有静态标志图形。构建正式工作台时，源码放在工程根目录的 `web/`。

## 系统设计

- [系统模块与服务边界](specs/omni-relay-module-design.md)
- [Go 主服务详细设计](specs/omni-relay-go-service-design.md)
- [Kubernetes 部署设计](specs/omni-relay-k8s-deployment-design.md)

## 品牌素材

- [双色横版 Logo](prototype/omni-relay-logo-bicolor.png)
- [飞书 Bot 头像](prototype/omni-relay-avatar.png)
- [CSS 色彩变量](prototype/omni-relay-colors.css)

OmniRelay 的当前选型与实施边界见 `specs/`。设计参考与原型引入的开源项目见 [开源项目与致谢](../README.md#开源项目与致谢)。
