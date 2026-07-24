import type { TFunction } from "i18next";
import {
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
} from "@/components/quota";
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from "@/stores";
import type { AuthFileItem } from "@/types";

type Notify = (message: string, tone?: "success" | "error" | "info" | "warning") => void;

const providerConfigs = [
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG,
] as const;

const resolveConfig = (file: AuthFileItem) => {
  const provider = String((file as { provider?: string }).provider || file.type || "").toLowerCase();
  return (
    providerConfigs.find((cfg) => cfg.type === provider) ||
    providerConfigs.find((cfg) => String(cfg.type).toLowerCase() === String(file.type || "").toLowerCase()) ||
    null
  );
};

const setterByType: Record<string, keyof ReturnType<typeof useQuotaStore.getState>> = {
  antigravity: "setAntigravityQuota",
  claude: "setClaudeQuota",
  codex: "setCodexQuota",
  "gemini-cli": "setGeminiCliQuota",
  gemini: "setGeminiCliQuota",
  kimi: "setKimiQuota",
  xai: "setXaiQuota",
};

export async function refreshVisibleQuotas(
  files: AuthFileItem[],
  showNotification: Notify,
  t: TFunction
): Promise<void> {
  let ok = 0;
  let fail = 0;
  const store = useQuotaStore.getState();

  for (const file of files) {
    if (file.disabled) continue;
    const config = resolveConfig(file);
    if (!config) continue;
    const setterName = setterByType[config.type] || setterByType[String(config.type).toLowerCase()];
    const setter = setterName ? (store as unknown as Record<string, unknown>)[setterName] : null;
    if (typeof setter !== "function") continue;
    const applySetter = setter as (updater: (prev: Record<string, unknown>) => Record<string, unknown>) => void;

    const cacheGeneration = captureQuotaCacheGeneration();
    try {
      applySetter((prev) => ({
        ...prev,
        [file.name]: config.buildLoadingState(),
      }));
      const data = await config.fetchQuota(file, t);
      // buildSuccessState 在联合配置上签名过窄，按具体 config 运行时分派
      const successState = (config.buildSuccessState as (payload: unknown) => unknown)(data);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        applySetter((prev) => ({
          ...prev,
          [file.name]: successState,
        }));
        ok += 1;
      });
    } catch (err: unknown) {
      fail += 1;
      const message = err instanceof Error ? err.message : String(err);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        applySetter((prev) => ({
          ...prev,
          [file.name]: config.buildErrorState(message),
        }));
      });
      showNotification(t("auth_files.quota_refresh_failed", { name: file.name, message }), "error");
    }
  }

  if (ok > 0) {
    showNotification(
      `已刷新本页 ${ok} 个账号额度` + (fail ? `，失败 ${fail} 个` : ""),
      fail ? "warning" : "success"
    );
  } else if (fail > 0) {
    showNotification(`本页额度刷新失败 ${fail} 个`, "error");
  } else {
    showNotification("当前页没有可刷新额度的铭牌", "info");
  }
}