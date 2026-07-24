# Docker 部署 CPA + grok-manager

grok-manager 已内置进 CPA，**任意基础镜像**（含 Alpine）只要运行本 monorepo 编译的 CPA，并在配置中启用即可。

```yaml
plugins:
  enabled: true
  configs:
    grok-manager:
      enabled: true
```

不必再下载或挂载 `.so` 插件文件。

若仍使用本目录 `Dockerfile.grok-manager`，它会拉取 release 中的 CPA 包；确保该 release 是包含 builtin 的新版本。
