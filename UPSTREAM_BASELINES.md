# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: c9417c8ae9b16fabc0386ca35d36f13bf8b1d678
- tag: v7.2.104
- note: selective merge from v7.2.102..v7.2.104: safe updates + pure new modules (request-lifecycle plugin, credential weight helpers/validation fields, home session alias, antigravity provenance/replay snapshot upgrade skipped to keep local antigravity executor chain, models/signature/translator fixes); kept local monoliths (conductor/selector/scheduler/auth_files/server/config/executors/handlers) to preserve packet/filter/cooldown/OpenWebUI/Grok Manager/quota chains; full weighted-RR scheduler path not force-ported into local selector/scheduler; management weight PATCH API partially not wired into local config_lists.

## Cli-Proxy-API-Management-Center
- branch: main
- commit: 1708314bc7a27e0ad9ef86b083e28e4e00aceeb1
- tag: v1.20.0
- note: selective merge from v1.19.3..v1.20.0: adopted dashboard feature rewrite + Claude Fable weekly quota display; merged i18n/format/quota helpers; kept local AuthFiles/packet/visual-config surfaces; added local first/last pagination and enabled_ok status filter on auth-files pages.

## CLIProxyAPI-Pro
- repository: https://github.com/ssfun/CLIProxyAPI-Pro
- branch: main
- commit: d013a136ea8d8541ac0ba480752a43527bed499f
- tag: v7.1.19-pro
- note: 2026-05-24 checked latest v7.1.19-pro / 6c42247177fee1661687e785a272a3c133852036, analysis only, not merged into current base
