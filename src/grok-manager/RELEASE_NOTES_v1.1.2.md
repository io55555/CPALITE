# v1.1.2 — 硬隔离 + Linux ARM64

## 变更

- **硬隔离**：`scheduler.pick` 一旦过滤到隔离号，不再 `Handled:false` 把选择权交回宿主（修复「全员 429 时宿主 fill-first 继续打已封号」）。
- 候选全部隔离时返回 `Handled:true` + 空 `AuthID`，业务路径不可再使用隔离凭证，直至解封。
- `isBannedCandidate` 补 `resolveEmailForAuth`，与 `usage.handle` 同一套邮箱匹配，减少漏跳。
- 测活 / `recheck429` 仍可直连探测（用于判断是否解封），与业务选号分离。
- **预编译 Linux arm64**：`grok-manager-linux-arm64.so`

## 安装

请到本页最下方 **Assets** 下载对应平台文件（可能需要展开/滚动）：

| 系统 | 文件 | 放到 CPA 目录 |
| --- | --- | --- |
| Windows amd64 | `grok-manager-v1.1.2.dll` 或 `grok-manager.dll` | `plugins/windows/amd64/` |
| Linux amd64 | `grok-manager-v1.1.2.so` 或 `grok-manager.so` 或 `grok-manager-linux-amd64.so` | `plugins/linux/amd64/` |
| **Linux arm64** | **`grok-manager-linux-arm64.so`** 或 `grok-manager-v1.1.2-linux-arm64.so` | **`plugins/linux/arm64/`**（可改名为 `grok-manager.so`） |

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    grok-manager:
      enabled: true
```

重启 CPA，日志应出现 `version=1.1.2`。
