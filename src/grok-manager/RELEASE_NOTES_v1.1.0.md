# grok-manager v1.1.0

完整公开版：测活 / 清理 / 运行时隔离，并开放 **SSO Cookie → CPA 转换** 与 **SSO 历史库**。

## 功能

- SSO Cookie 批量转 CPA xAI OAuth 凭证
- SSO 历史库（vault）持久化、分页、导出、删除
- 401 从历史库自动重刷
- 定时管线：扫描 → 复检 → 可选 401 重刷
- 429 固定隔离 2 小时（硬顶），到期自动复测
- 隔离按邮箱主键去重；usage 无邮箱时从 auth 反填

## 安装（Windows）

```text
plugins/windows/amd64/grok-manager.dll
# 或
plugins/windows/amd64/grok-manager-v1.1.0.dll
```

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    grok-manager:
      enabled: true
```

重启 CPA，日志中应出现：`plugin_id=grok-manager version=1.1.0`
