# Grok Manager v1.1.3

## 变更

- 管理密钥支持 **显示 / 隐藏**（文字按钮，Edge / Chrome 可用）
- 隐藏浏览器自带密码小眼睛，避免和自带按钮冲突
- 界面标题统一为 **Grok Manager**
- 继承 v1.1.2 硬隔离：账号 429 / 封禁后，`scheduler.pick` 返回 `Handled:true`，不会再退回全量 banned 池乱选

## 下载

- Linux amd64：`grok-manager-linux-amd64.so` / `grok-manager.so`
- Linux arm64：`grok-manager-linux-arm64.so`
- Windows amd64：`grok-manager-windows-amd64.dll` / `grok-manager.dll`

## 安装

把对应平台的 `.so` / `.dll` 放到 CLIProxyAPI 的 `plugins/` 目录，启用 **grok-manager** 即可。
