/**
 * AI 提供商相关类型
 * 基于原项目 src/modules/ai-providers.js
 */

export interface ModelAlias {
  name: string;
  alias?: string;
  priority?: number;
  testModel?: string;
}

export interface ApiKeyEntry {
  apiKey: string;
  proxyUrl?: string;
  headers?: Record<string, string>;
  authIndex?: string;
}

export interface OpenAIStatusRuler {
  name: string;
  when: {
    status: number;
    'json-path'?: string;
    'json-equals'?: string;
    'json-contains'?: string;
    'body-equals'?: string;
    'body-contains'?: string;
  };
  action: string;
  'client-status'?: number;
  'client-message'?: string;
}

export interface OpenAIKeyState {
  provider_name: string;
  api_key: string;
  enabled: boolean;
  status: string;
  status_message?: string;
  frozen_until?: string;
  last_error?: string;
  raw_request?: string;
  raw_response?: string;
  updated_at?: string;
}

export interface CloakConfig {
  mode?: string;
  strictMode?: boolean;
  sensitiveWords?: string[];
}

export interface GeminiKeyConfig {
  apiKey: string;
  disabled?: boolean;
  priority?: number;
  prefix?: string;
  baseUrl?: string;
  proxyUrl?: string;
  models?: ModelAlias[];
  headers?: Record<string, string>;
  excludedModels?: string[];
  authIndex?: string;
}

export interface ProviderKeyConfig {
  apiKey: string;
  priority?: number;
  prefix?: string;
  baseUrl?: string;
  websockets?: boolean;
  proxyUrl?: string;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  excludedModels?: string[];
  cloak?: CloakConfig;
  authIndex?: string;
}

export interface OpenAIProviderConfig {
  name: string;
  prefix?: string;
  baseUrl: string;
  apiKeyEntries: ApiKeyEntry[];
  disabled?: boolean;
  headers?: Record<string, string>;
  models?: ModelAlias[];
  priority?: number;
  testModel?: string;
  statusRulers?: OpenAIStatusRuler[];
  authIndex?: string;
  [key: string]: unknown;
}
