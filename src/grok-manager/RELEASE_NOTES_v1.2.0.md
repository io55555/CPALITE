# Grok Manager v1.2.0

## 变更

- 前端按软拟态 SaaS 风格重做：胶囊按钮、指标卡、进度环、圆角搜索与筛选
- 方案 B：测活结束后自动对账隔离（坏状态写入/续期，健康自动解禁）
- 测活页支持「同步到隔离」、行内删除凭证
- 隔离页：解禁 / 删除凭证 / 清理幽灵记录；凭证删除后可同步隔离表
- 导航收敛为：隔离 · 测活 · 入库（SSO + 历史库）
- 唯一插件名 `grok-manager`（不再分 CPA 私有版）

## 下载

- Linux amd64：`grok-manager-linux-amd64.so` / `grok-manager.so`
- Linux arm64：`grok-manager-linux-arm64.so`
- Windows amd64：`grok-manager-windows-amd64.dll` / `grok-manager.dll`

## 安装

放到 CLIProxyAPI 的 `plugins/` 目录，只启用 **grok-manager**。
