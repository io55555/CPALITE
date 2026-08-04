/**
 * 配置相关类型定义
 * 与基线 /config 返回结构保持一致（内部使用驼峰形式）
 */

import type { AmpcodeConfig } from './ampcode';
import type { GeminiKeyConfig, ProviderKeyConfig, OpenAIProviderConfig } from './provider';

export interface QuotaExceededConfig {
  switchProject?: boolean;
  switchPreviewModel?: boolean;
  antigravityCredits?: boolean;
}

export interface AuthPoolCleanConfig {
  baseUrl?: string;
  token?: string;
  targetType?: string;
  workers?: number;
  deleteWorkers?: number;
  timeout?: number;
  retries?: number;
  userAgent?: string;
  usedPercentThreshold?: number;
  sampleSize?: number;
}

export interface XaiConfig {
  injectXSearch?: boolean;
}

export interface Config {
  debug?: boolean;
  proxyUrl?: string;
  requestRetry?: number;
  quotaCooldownBaseSeconds?: number;
  quotaCooldownMaxSeconds?: number;
  proxyFailureCooldownSeconds?: number;
  proxyFailureMaxCooldownSeconds?: number;
  quotaExceeded?: QuotaExceededConfig;
  clean?: AuthPoolCleanConfig;
  usageStatisticsEnabled?: boolean;
  redisUsageQueueRetentionSeconds?: number;
  requestLog?: boolean;
  loggingToFile?: boolean;
  logsMaxTotalSizeMb?: number;
  wsAuth?: boolean;
  forceModelPrefix?: boolean;
  xaiGrokBuildHeaderDefaults?: boolean;
  xaiGrokBuildHeaderDefaultsUserAgent?: string;
  xaiOpenWebUICompat?: boolean;
  routingStrategy?: string;
  apiKeys?: string[];
  ampcode?: AmpcodeConfig;
  xai?: XaiConfig;
  geminiApiKeys?: GeminiKeyConfig[];
  codexApiKeys?: ProviderKeyConfig[];
  xaiApiKeys?: ProviderKeyConfig[];
  claudeApiKeys?: ProviderKeyConfig[];
  vertexApiKeys?: ProviderKeyConfig[];
  openaiCompatibility?: OpenAIProviderConfig[];
  oauthExcludedModels?: Record<string, string[]>;
  raw?: Record<string, unknown>;
}

export type RawConfigSection =
  | 'debug'
  | 'proxy-url'
  | 'request-retry'
  | 'quota-exceeded'
  | 'request-log'
  | 'logging-to-file'
  | 'logs-max-total-size-mb'
  | 'ws-auth'
  | 'force-model-prefix'
  | 'xai-grok-build-header-defaults'
  | 'xai-grok-build-header-defaults-user-agent'
  | 'xai-openwebui-compat'
  | 'xai'
  | 'routing/strategy'
  | 'api-keys'
  | 'ampcode'
  | 'gemini-api-key'
  | 'codex-api-key'
  | 'xai-api-key'
  | 'claude-api-key'
  | 'vertex-api-key'
  | 'openai-compatibility'
  | 'oauth-excluded-models';
