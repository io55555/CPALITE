# Upstream Baselines

## CLIProxyAPI
- branch: main
- commit: 323b7276bc5bd251e5497699e42c556d6316b30c
- tag: v7.2.131
- note: selective merge from v7.2.118..v7.2.131: adopted compatible registry/model, translator, request-retry/is-compat, clienterror, Codex live/realtime, Infistar docs assets, and focused tests while keeping local conductor/selector/scheduler/auth-files/server/config/executor/packet-filter/Grok Manager surfaces where required to preserve cooldown, OpenWebUI, quota, monitoring, SSOcookie, and packet-capture chains. Official Antigravity provenance/replay snapshot rewrite was not adopted.

## Cli-Proxy-API-Management-Center
- branch: main
- commit: f60c8ca683b118be5750ff102187cc6d8ad4605b
- tag: v1.22.2
- note: selective merge from v1.21.4..v1.22.2: adopted the redesigned features/config page, Infistar provider source, API key strength utilities, and compatible i18n updates while keeping local packet/filter/auth cooldown/Grok Manager/custom config/monitoring surfaces; xAI Grok Build/OpenWebUI and Codex identity controls remain local and are surfaced on the new config page. Infistar is present as source/tests but not fully wired into the local provider workbench.

## CLIProxyAPI-Pro
- repository: https://github.com/ssfun/CLIProxyAPI-Pro
- branch: main
- commit: d013a136ea8d8541ac0ba480752a43527bed499f
- tag: v7.1.19-pro
- note: 2026-05-24 checked latest v7.1.19-pro / 6c42247177fee1661687e785a272a3c133852036, analysis only, not merged into current base
