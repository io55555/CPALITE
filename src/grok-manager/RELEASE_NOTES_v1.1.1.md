# grok-manager v1.1.1

修复发布包与安装说明：补齐 **Linux 预编译**，并说明「已配置 / 未注册 / 未生效」如何处理。

## 变更

- 发布资产同时提供：
  - Windows：`grok-manager.dll` / `grok-manager-windows-amd64.dll` / `grok-manager-v1.1.1.dll`
  - Linux：`grok-manager.so` / `grok-manager-linux-amd64.so` / `grok-manager-v1.1.1.so`
- 增加 GitHub Actions 自动构建双平台
- README 补充生效步骤与状态对照

功能与 v1.1.0 相同（测活 / 清理 / 运行时隔离 / SSO / vault）。

## 安装

### Windows

```text
plugins/windows/amd64/grok-manager.dll
```

### Linux

```text
plugins/linux/amd64/grok-manager.so
```

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    grok-manager:
      enabled: true
```

**必须重启 CPA。** 日志应出现：`plugin_id=grok-manager version=1.1.1`

若面板显示「已配置 / 未注册」：说明配置已写入，但当前系统的动态库缺失或路径不对，按上表放入对应文件后重启即可。
