# Grok Manager v1.2.4

## 变更

- 隔离页支持对**已选 / 单条 / 按状态**凭证测活（不限 429）
- 新增**测活记录**历史：每次测活结果落盘并可回看明细
- 记录文件：`plugins/grok-manager/probe-history.json`（约 40 次）
- 接口：`POST /bans-probe`、`GET /bans-probe-history`

## 下载

- Linux amd64：`grok-manager-linux-amd64.so` / `grok-manager.so`
- Linux arm64：`grok-manager-linux-arm64.so`
- Windows amd64：`grok-manager-windows-amd64.dll` / `grok-manager.dll`

## 安装

放到 CLIProxyAPI `plugins/`，只启用 **grok-manager**。
