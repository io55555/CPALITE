# grok-manager v1.0.0

首个公开发布版本。

## 包含

- xAI 凭证并发测活（分页结果）
- 按候选 / HTTP 状态 / 文件名删除
- 运行时隔离（401 / 402 / 403 / 429）
- 429 固定 2h + 到期复测
- 定时扫描与可选复检
- 管理面板（总览 / 测活 / 隔离 / 定时）
- 数据备份（scan + schedule + bans）

## 不包含

- SSO Cookie 转 CPA 凭证
- SSO 历史库 / 401 自动从库重刷

## 安装

将 `grok-manager.dll`（Windows）或 `grok-manager.so`（Linux）放入：

```text
plugins/<os>/<arch>/
```

并在配置中启用 `plugins.configs.grok-manager.enabled: true`，重启 CPA。

详见 [README.md](README.md)。
