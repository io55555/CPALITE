import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { IconMinus } from '@/components/ui/icons';
import { getAuthFileStatusMessage } from '@/features/authFiles/constants';
import { useInterval } from '@/hooks/useInterval';
import { authFilesApi } from '@/services/api/authFiles';
import { logsApi } from '@/services/api/logs';
import { useNotificationStore, useUsageStatsStore } from '@/stores';
import type { GeminiKeyConfig, ProviderKeyConfig, OpenAIProviderConfig } from '@/types';
import type { AuthFileItem } from '@/types/authFile';
import type { CredentialInfo } from '@/types/sourceInfo';
import { buildSourceInfoMap, resolveSourceDisplay } from '@/utils/sourceResolver';
import { parseTimestampMs } from '@/utils/timestamp';
import {
  collectUsageDetails,
  extractFirstByteLatencyMs,
  extractGenerationMs,
  extractTotalTokens,
  formatDurationMs,
  normalizeAuthIndex,
  type UsageThinking,
} from '@/utils/usage';
import { downloadBlob } from '@/utils/download';
import styles from '@/pages/UsagePage.module.scss';

const ALL_FILTER = '__all__';
const RESULT_SUCCESS_FILTER = 'success';
const RESULT_FAILURE_FILTER = 'failure';
const MAX_RENDERED_EVENTS = 500;

type RequestEventRow = {
  id: string;
  backendId: string | null;
  requestId: string | null;
  endpoint: string;
  timestamp: string;
  timestampMs: number;
  timestampLabel: string;
  model: string;
  provider: string;
  sourceKey: string;
  sourceRaw: string;
  source: string;
  sourceType: string;
  authType: string;
  authIndex: string;
  failed: boolean;
  firstByteLatencyMs: number | null;
  generationMs: number | null;
  tps: number | null;
  thinking: UsageThinking | null;
  thinkingLabel: string;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  totalTokens: number;
  cacheHitRatio: number | null;
  rawRequest?: string;
  rawResponse?: string;
  failureStatusCode?: number;
  failureMessage?: string;
};

const extractLogSection = (content: string, title: string, nextTitle?: string): string => {
  let start = content.indexOf(title);
  let titleLength = title.length;
  if (start < 0 && title.endsWith(' ===')) {
    const prefix = title.slice(0, -4);
    const regex = new RegExp(`${prefix.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s+\\d+\\s+===`);
    const match = content.match(regex);
    if (match?.index !== undefined) {
      start = match.index;
      titleLength = match[0].length;
    }
  }
  if (start < 0) return '';
  const from = start + titleLength;
  const end = nextTitle ? content.indexOf(nextTitle, from) : -1;
  return content.slice(from, end >= 0 ? end : undefined).trim();
};

const extractPacketBody = (packet: string): string => {
  const trimmed = packet.trim();
  if (!trimmed) return '';
  const separatorMatch = trimmed.match(/\r?\n\r?\n/);
  if (!separatorMatch) return trimmed;
  return trimmed.slice(separatorMatch.index! + separatorMatch[0].length).trim();
};

const extractNamedSection = (content: string, title: string): string => {
  const marker = `=== ${title} ===`;
  const start = content.indexOf(marker);
  if (start < 0) return '';
  const from = start + marker.length;
  const next = content.indexOf('=== ', from);
  return content.slice(from, next >= 0 ? next : undefined).trim();
};

const firstNonEmpty = (...values: string[]): string => {
  for (const value of values) {
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return '';
};

const parseHTTPStatusCode = (packet: string, fallback?: number | null): number | null => {
  const match = packet.match(/^HTTP\/\d(?:\.\d)?\s+(\d{3})/m);
  if (match?.[1]) return Number(match[1]);
  return fallback && fallback > 0 ? fallback : null;
};

const extractFailureMessageFromJson = (text: string): string => {
  try {
    const parsed = JSON.parse(text) as {
      error?: { message?: unknown; code?: unknown; type?: unknown };
      message?: unknown;
      error_message?: unknown;
    };
    const direct =
      parsed.error?.message ?? parsed.message ?? parsed.error_message ?? parsed.error?.code ?? parsed.error?.type;
    if (typeof direct === 'string' && direct.trim()) {
      return direct.trim();
    }
  } catch {
    // 响应体可能不是 JSON。
  }
  return '';
};

const extractFailureLine = (text: string): string => {
  const trimmed = text.trim();
  if (!trimmed) return '';
  const lines = trimmed.split(/\r?\n/);
  for (const prefix of ['Failure Message:', 'Error:']) {
    const line = lines.find((item) => item.trim().startsWith(prefix));
    if (line) {
      return line.slice(line.indexOf(prefix) + prefix.length).trim();
    }
  }
  return '';
};

const summarizeFailureReason = (
  message: string,
  statusCode?: number | null
): string => {
  const trimmed = message.trim();
  if (!trimmed) return '';
  const lower = trimmed.toLowerCase();
  if (trimmed.includes('未知失败原因') && trimmed.includes('未捕获到上游错误正文')) {
    return '';
  }

  const withDetail = (label: string) => (trimmed ? `${label}: ${trimmed}` : label);

  if (lower.includes('missing provider baseurl') || lower.includes('missing provider base url')) {
    return withDetail('上游 baseURL 配置错误');
  }
  if (
    lower.includes('missing provider api key') ||
    lower.includes('missing api key') ||
    lower.includes('api_key required') ||
    lower.includes('api key required') ||
    lower.includes('no api key')
  ) {
    return withDetail('无 API Key');
  }
  if (
    lower.includes('proxy error') ||
    lower.includes('proxy_or_network_error') ||
    lower.includes('dial tcp') ||
    lower.includes('connectex') ||
    lower.includes('connection refused') ||
    lower.includes('connection reset') ||
    lower.includes('no such host') ||
    lower.includes('i/o timeout') ||
    lower.includes('context deadline exceeded') ||
    lower.includes('tls handshake') ||
    lower.includes('eof')
  ) {
    return withDetail('网络故障或代理 IP 故障');
  }
  if (
    lower.includes('missing auth') ||
    lower.includes('no auth') ||
    lower.includes('no credential') ||
    lower.includes('credential unavailable') ||
    lower.includes('no available auth')
  ) {
    return withDetail('无可用账号');
  }
  if (
    lower.includes('wrong api key') ||
    lower.includes('invalid api key') ||
    lower.includes('incorrect api key') ||
    (lower.includes('api key') && lower.includes('unauthorized'))
  ) {
    return withDetail('API Key 无效');
  }
  if (
    lower.includes('model') &&
    (lower.includes('not found') ||
      lower.includes('does not exist') ||
      lower.includes('unsupported') ||
      lower.includes('invalid') ||
      lower.includes('unavailable') ||
      lower.includes('unknown'))
  ) {
    return withDetail('无匹配模型或模型不可用');
  }
  if (
    lower.includes('quota') ||
    lower.includes('exhaust') ||
    lower.includes('insufficient balance') ||
    lower.includes('credit')
  ) {
    return withDetail('账号额度不足');
  }
  if (
    statusCode === 429 ||
    lower.includes('rate limit') ||
    lower.includes('too many requests')
  ) {
    return withDetail('触发限流');
  }
  if (statusCode === 401 || statusCode === 403 || lower.includes('forbidden') || lower.includes('unauthorized')) {
    return withDetail('鉴权失败');
  }
  return trimmed;
};

const summarizeMissingFailureReason = (
  row: RequestEventRow | null,
  credentialInfo?: CredentialInfo
): string => {
  if (!row) return '';
  const statusCode = row.failureStatusCode ?? null;
  if (statusCode === 401 || statusCode === 403) {
    return '鉴权失败，账号或 API Key 无效';
  }
  if (statusCode === 404) {
    return '无匹配模型或上游接口不存在';
  }
  if (statusCode === 407) {
    return '代理服务器认证失败';
  }
  if (statusCode === 408 || statusCode === 504) {
    return '网络超时或代理 IP 故障';
  }
  if (statusCode === 429) {
    return '触发限流或账号额度不足';
  }
  if (statusCode !== null && statusCode >= 500) {
    return '上游服务异常或代理 IP 故障';
  }
  if (
    row.firstByteLatencyMs === 0 &&
    row.inputTokens === 0 &&
    row.outputTokens === 0 &&
    row.totalTokens === 0
  ) {
    return '网络故障、代理 IP 故障或上游连接失败：请求未收到可解析的上游响应正文';
  }
  if (row.sourceType === 'openai' && row.model && row.model !== '-') {
    return `OpenAI 兼容上游请求失败：请检查账号/API Key、代理 IP、baseURL 与模型 ${row.model} 是否匹配`;
  }
  const statusMessage = credentialInfo?.statusMessage?.trim();
  if (statusMessage) {
    return `凭证当前状态异常: ${statusMessage}`;
  }
  return '请求失败：当前历史记录缺少上游错误正文，无法进一步区分账号、API Key、代理 IP 或模型配置';
};

const isProxyOrNetworkFailure = (row: RequestEventRow): boolean => {
  const text = `${row.failureMessage ?? ''}\n${row.rawResponse ?? ''}`.toLowerCase();
  return (
    text.includes('proxy_or_network_error') ||
    text.includes('proxy') ||
    text.includes('dial tcp') ||
    text.includes('connectex') ||
    text.includes('connection refused') ||
    text.includes('connection reset') ||
    text.includes('i/o timeout') ||
    text.includes('no such host') ||
    text.includes('tls handshake')
  );
};

const statusRulerActionLabel = (detail: string): string => {
  const action = detail.match(/动作:\s*([^\r\n]+)/)?.[1]?.trim() ?? '';
  if (action.includes('禁用')) return '禁用';
  if (action.includes('冷却')) return '冷却';
  return action || '';
};

const formatResultLabel = (row: RequestEventRow): string => {
  if (!row.failed) return '成功';
  const status = parseHTTPStatusCode(
    extractNamedSection(row.rawResponse ?? '', '供应商返回CPA的完整数据包') || (row.rawResponse ?? ''),
    row.failureStatusCode ?? null
  );
  const base = isProxyOrNetworkFailure(row) && !status ? '失败 ip' : `失败${status ? ` ${status}` : ''}`;
  const rulerAction = statusRulerActionLabel(extractNamedSection(row.rawResponse ?? '', '触发status-rulers'));
  return rulerAction ? `${base} ${rulerAction}` : base;
};

const requestPathFromEndpoint = (endpoint: string): string => {
  const trimmed = endpoint.trim();
  if (!trimmed || trimmed === '-') return '/v1/chat/completions';
  const parts = trimmed.split(/\s+/);
  if (parts.length >= 2 && parts[1].startsWith('/')) return parts[1];
  return trimmed.startsWith('/') ? trimmed : '/v1/chat/completions';
};

const buildFallbackRequestPacket = (row: RequestEventRow): string =>
  [
    `POST ${requestPathFromEndpoint(row.endpoint)} HTTP/2`,
    'content-type: application/json',
    '',
    JSON.stringify({ model: row.model === '-' ? undefined : row.model }),
    '',
    '# 该历史记录未保存完整请求体；以上仅保留请求包格式，完整失败记录如下。',
    JSON.stringify(row, null, 2),
  ].join('\n');

const buildFallbackResponsePacket = (row: RequestEventRow, message: string): string => {
  const status = row.failureStatusCode && row.failureStatusCode > 0 ? row.failureStatusCode : 502;
  return [
    `HTTP/1.1 ${status}`,
    'content-type: text/plain; charset=utf-8',
    '',
    message || 'unknown failure',
    '',
    '# 该历史记录未保存完整响应体；以上仅保留响应包格式，完整失败记录如下。',
    JSON.stringify(row, null, 2),
  ].join('\n');
};

export interface RequestEventsDetailsCardProps {
  usage: unknown;
  loading: boolean;
  geminiKeys: GeminiKeyConfig[];
  claudeConfigs: ProviderKeyConfig[];
  codexConfigs: ProviderKeyConfig[];
  vertexConfigs: ProviderKeyConfig[];
  openaiProviders: OpenAIProviderConfig[];
  authFiles?: AuthFileItem[];
  onRefresh?: () => Promise<void> | void;
  lastRefreshedAt?: Date | null;
  fixedHeight?: boolean;
}

const AUTO_REFRESH_OFF = 'off';
const AUTO_REFRESH_CUSTOM = 'custom';
const AUTO_REFRESH_INTERVALS = {
  '15s': 15_000,
  '30s': 30_000,
  '1m': 60_000,
  '5m': 300_000,
} as const;
const MIN_CUSTOM_AUTO_REFRESH_SECONDS = 5;
const MAX_CUSTOM_AUTO_REFRESH_SECONDS = 3600;
const DEFAULT_CUSTOM_AUTO_REFRESH_SECONDS = 60;

type AutoRefreshValue =
  | keyof typeof AUTO_REFRESH_INTERVALS
  | typeof AUTO_REFRESH_OFF
  | typeof AUTO_REFRESH_CUSTOM;

const toNumber = (value: unknown): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return 0;
  return parsed;
};

const normalizeCustomAutoRefreshSeconds = (value: unknown): number => {
  const parsed = Math.floor(Number(value));
  if (!Number.isFinite(parsed)) {
    return DEFAULT_CUSTOM_AUTO_REFRESH_SECONDS;
  }
  return Math.min(Math.max(parsed, MIN_CUSTOM_AUTO_REFRESH_SECONDS), MAX_CUSTOM_AUTO_REFRESH_SECONDS);
};

const normalizeThinkingText = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  return value.trim();
};

const formatThinkingLabel = (thinking: UsageThinking | null): string => {
  if (!thinking) return '-';

  const intensity = normalizeThinkingText(thinking.intensity);
  const level = normalizeThinkingText(thinking.level);
  const mode = normalizeThinkingText(thinking.mode);
  const budget =
    typeof thinking.budget === 'number' && Number.isFinite(thinking.budget)
      ? thinking.budget
      : null;
  const label = intensity || level || (budget !== null ? String(budget) : mode);
  const budgetLabel = budget !== null ? budget.toLocaleString() : null;

  if (!label) return '-';
  if (budgetLabel !== null && label === String(budget)) {
    return budgetLabel;
  }
  if (mode === 'budget' && budget !== null && budget > 0) {
    return `${label} (${budgetLabel})`;
  }
  if (budget === -1 && label !== 'auto') {
    return `${label} (-1)`;
  }
  return label;
};

const formatCacheHitRatio = (ratio: number | null): string => {
  if (ratio === null) return '--';
  return `${(ratio * 100).toFixed(1)}%`;
};

const encodeCsv = (value: string | number): string => {
  const text = String(value ?? '');
  const trimmedLeft = text.replace(/^\s+/, '');
  const safeText = trimmedLeft && /^[=+\-@]/.test(trimmedLeft) ? `'${text}` : text;
  return `"${safeText.replace(/"/g, '""')}"`;
};

export function RequestEventsDetailsCard({
  usage,
  loading,
  geminiKeys,
  claudeConfigs,
  codexConfigs,
  vertexConfigs,
  openaiProviders,
  authFiles,
  onRefresh,
  lastRefreshedAt,
  fixedHeight = false,
}: RequestEventsDetailsCardProps) {
  const { t, i18n } = useTranslation();
  const { showConfirmation, showNotification } = useNotificationStore();
  const deleteUsageRecords = useUsageStatsStore((state) => state.deleteUsageRecords);
  const deleteAllUsageRecords = useUsageStatsStore((state) => state.deleteAllUsageRecords);

  const [modelFilter, setModelFilter] = useState(ALL_FILTER);
  const [sourceFilter, setSourceFilter] = useState(ALL_FILTER);
  const [resultFilter, setResultFilter] = useState(ALL_FILTER);
  const [autoRefreshValue, setAutoRefreshValue] = useState<AutoRefreshValue>(AUTO_REFRESH_OFF);
  const [customAutoRefreshSeconds, setCustomAutoRefreshSeconds] = useState(
    DEFAULT_CUSTOM_AUTO_REFRESH_SECONDS.toString()
  );
  const [localAuthFiles, setLocalAuthFiles] = useState<AuthFileItem[]>([]);
  const [selectedFailureRow, setSelectedFailureRow] = useState<RequestEventRow | null>(null);
  const [selectedFailureLogText, setSelectedFailureLogText] = useState('');
  const [selectedFailureLogLoading, setSelectedFailureLogLoading] = useState(false);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [selectedRowIds, setSelectedRowIds] = useState<Record<string, true>>({});
  const [nextRefreshAtMs, setNextRefreshAtMs] = useState<number | null>(null);
  const [countdownNowMs, setCountdownNowMs] = useState(() => Date.now());

  const resolvedAuthFiles = authFiles ?? localAuthFiles;

  const refreshAuthFiles = useCallback(async () => {
    if (authFiles) return;
    try {
      const res = await authFilesApi.list();
      const files = Array.isArray(res) ? res : (res as { files?: AuthFileItem[] })?.files;
      if (!Array.isArray(files)) return;
      setLocalAuthFiles(files);
    } catch {
      // Ignore auth file refresh failures.
    }
  }, [authFiles]);

  useEffect(() => {
    if (authFiles) return;
    void refreshAuthFiles();
  }, [authFiles, refreshAuthFiles]);

  useEffect(() => {
    if (authFiles || !lastRefreshedAt) {
      return;
    }
    void refreshAuthFiles();
  }, [authFiles, lastRefreshedAt, refreshAuthFiles]);

  const authFileMap = useMemo(() => {
    const map = new Map<string, CredentialInfo>();
    resolvedAuthFiles.forEach((file) => {
      const key = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
      if (!key) return;
      map.set(key, {
        name: file.name || key,
        type: (file.type || file.provider || '').toString(),
        statusMessage: getAuthFileStatusMessage(file),
      });
    });
    return map;
  }, [resolvedAuthFiles]);

  const sourceInfoMap = useMemo(
    () =>
      buildSourceInfoMap({
        geminiApiKeys: geminiKeys,
        claudeApiKeys: claudeConfigs,
        codexApiKeys: codexConfigs,
        vertexApiKeys: vertexConfigs,
        openaiCompatibility: openaiProviders,
      }),
    [claudeConfigs, codexConfigs, geminiKeys, openaiProviders, vertexConfigs]
  );

  const autoRefreshOptions = useMemo(
    () => [
      { value: AUTO_REFRESH_OFF, label: t('monitoring_center.auto_refresh_off') },
      { value: '15s', label: '15s' },
      { value: '30s', label: '30s' },
      { value: '1m', label: '1m' },
      { value: '5m', label: '5m' },
      { value: AUTO_REFRESH_CUSTOM, label: t('monitoring_center.auto_refresh_custom') }
    ],
    [t]
  );
  const normalizedCustomAutoRefreshSeconds = useMemo(
    () => normalizeCustomAutoRefreshSeconds(customAutoRefreshSeconds),
    [customAutoRefreshSeconds]
  );
  const autoRefreshDelay = useMemo(() => {
    if (!onRefresh || autoRefreshValue === AUTO_REFRESH_OFF) {
      return null;
    }
    if (autoRefreshValue === AUTO_REFRESH_CUSTOM) {
      return normalizedCustomAutoRefreshSeconds * 1000;
    }
    return AUTO_REFRESH_INTERVALS[autoRefreshValue];
  }, [autoRefreshValue, normalizedCustomAutoRefreshSeconds, onRefresh]);

  useEffect(() => {
    if (!autoRefreshDelay) {
      setNextRefreshAtMs(null);
      return;
    }

    const now = Date.now();
    setCountdownNowMs(now);

    const nextFromRefresh = lastRefreshedAt ? lastRefreshedAt.getTime() + autoRefreshDelay : null;
    const nextRefreshAt =
      nextFromRefresh && nextFromRefresh > now ? nextFromRefresh : now + autoRefreshDelay;

    setNextRefreshAtMs(nextRefreshAt);
  }, [autoRefreshDelay, lastRefreshedAt]);

  useInterval(() => {
    setCountdownNowMs(Date.now());
  }, autoRefreshDelay ? 1000 : null);

  const handleCustomAutoRefreshSecondsChange = useCallback((value: string) => {
    setCustomAutoRefreshSeconds(value.replace(/\D/g, ''));
  }, []);

  const handleCustomAutoRefreshSecondsBlur = useCallback(() => {
    setCustomAutoRefreshSeconds(normalizeCustomAutoRefreshSeconds(customAutoRefreshSeconds).toString());
  }, [customAutoRefreshSeconds]);

  useInterval(() => {
    if (!onRefresh || loading || !autoRefreshDelay) return;
    setNextRefreshAtMs(Date.now() + autoRefreshDelay);
    void onRefresh();
  }, autoRefreshDelay);

  const autoRefreshCountdown =
    autoRefreshDelay && nextRefreshAtMs
      ? Math.max(0, Math.ceil((nextRefreshAtMs - countdownNowMs) / 1000))
      : null;

  const rows = useMemo<RequestEventRow[]>(() => {
    const details = collectUsageDetails(usage);

    const baseRows = details.map((detail, index) => {
      const timestamp = detail.timestamp;
      const timestampMs =
        typeof detail.__timestampMs === 'number' && detail.__timestampMs > 0
          ? detail.__timestampMs
          : parseTimestampMs(timestamp);
      const date = Number.isNaN(timestampMs) ? null : new Date(timestampMs);
      const sourceRaw = String(detail.source ?? '').trim();
      const authIndexRaw = detail.auth_index as unknown;
      const authIndex =
        authIndexRaw === null || authIndexRaw === undefined || authIndexRaw === ''
          ? '-'
          : String(authIndexRaw);
      const sourceInfo = resolveSourceDisplay(sourceRaw, authIndexRaw, sourceInfoMap, authFileMap);
      const source = sourceInfo.displayName;
      const sourceKey = sourceInfo.identityKey ?? `source:${sourceRaw || source}`;
      const sourceType = sourceInfo.type;
      const model = String(detail.__modelName ?? '').trim() || '-';
      const inputTokens = Math.max(toNumber(detail.tokens?.input_tokens), 0);
      const outputTokens = Math.max(toNumber(detail.tokens?.output_tokens), 0);
      const reasoningTokens = Math.max(toNumber(detail.tokens?.reasoning_tokens), 0);
      const cachedTokens = Math.max(
        Math.max(toNumber(detail.tokens?.cached_tokens), 0),
        Math.max(toNumber(detail.tokens?.cache_tokens), 0)
      );
      const totalTokens = Math.max(
        toNumber(detail.tokens?.total_tokens),
        extractTotalTokens(detail)
      );
      const backendId = typeof detail.id === 'string' && detail.id.trim() ? detail.id.trim() : null;
      const firstByteLatencyMs = extractFirstByteLatencyMs(detail);
      const generationMs = extractGenerationMs(detail);
      const tps = generationMs && generationMs > 0 ? outputTokens / (generationMs / 1000) : null;
      const thinking = detail.thinking ?? null;
      const thinkingEffort = normalizeThinkingText(detail.thinking_effort);
      const thinkingLabel = thinkingEffort || formatThinkingLabel(thinking);
      const cacheHitRatio = inputTokens > 0 ? cachedTokens / inputTokens : null;

      return {
        id: backendId ?? `${timestamp}-${model}-${sourceKey}-${authIndex}-${index}`,
        backendId,
        requestId: typeof detail.request_id === 'string' && detail.request_id.trim() ? detail.request_id.trim() : null,
        endpoint: typeof detail.endpoint === 'string' && detail.endpoint.trim() ? detail.endpoint.trim() : '-',
        timestamp,
        timestampMs: Number.isNaN(timestampMs) ? 0 : timestampMs,
        timestampLabel: date ? date.toLocaleString(i18n.language) : timestamp || '-',
        model,
        provider: typeof detail.provider === 'string' && detail.provider.trim() ? detail.provider.trim() : '-',
        sourceKey,
        sourceRaw: sourceRaw || '-',
        source,
        sourceType,
        authType: typeof detail.auth_type === 'string' && detail.auth_type.trim() ? detail.auth_type.trim() : '-',
        authIndex,
        failed: detail.failed === true,
        firstByteLatencyMs,
        generationMs,
        tps,
        thinking,
        thinkingLabel,
        inputTokens,
        outputTokens,
        reasoningTokens,
        cachedTokens,
        totalTokens,
        cacheHitRatio,
        rawRequest: detail.raw_request,
        rawResponse: detail.raw_response,
        failureStatusCode: detail.failure_status_code,
        failureMessage: detail.failure_message,
      };
    });

    const sourceLabelKeyMap = new Map<string, Set<string>>();
    baseRows.forEach((row) => {
      const keys = sourceLabelKeyMap.get(row.source) ?? new Set<string>();
      keys.add(row.sourceKey);
      sourceLabelKeyMap.set(row.source, keys);
    });

    const buildDisambiguatedSourceLabel = (row: RequestEventRow) => {
      const labelKeyCount = sourceLabelKeyMap.get(row.source)?.size ?? 0;
      if (labelKeyCount <= 1) {
        return row.source;
      }

      if (row.authIndex !== '-') {
        return `${row.source} · ${row.authIndex}`;
      }

      if (row.sourceRaw !== '-' && row.sourceRaw !== row.source) {
        return `${row.source} · ${row.sourceRaw}`;
      }

      if (row.sourceType) {
        return `${row.source} · ${row.sourceType}`;
      }

      return `${row.source} · ${row.sourceKey}`;
    };

    return baseRows
      .map((row) => ({
        ...row,
        source: buildDisambiguatedSourceLabel(row),
      }))
      .sort((a, b) => b.timestampMs - a.timestampMs);
  }, [authFileMap, i18n.language, sourceInfoMap, usage]);

  const hasTimingData = useMemo(
    () => rows.some((row) => row.firstByteLatencyMs !== null || row.generationMs !== null),
    [rows]
  );

  const modelOptions = useMemo(
    () => [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      ...Array.from(new Set(rows.map((row) => row.model))).map((model) => ({
        value: model,
        label: model,
      })),
    ],
    [rows, t]
  );

  const sourceOptions = useMemo(() => {
    const optionMap = new Map<string, string>();
    rows.forEach((row) => {
      if (!optionMap.has(row.sourceKey)) {
        optionMap.set(row.sourceKey, row.source);
      }
    });

    return [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      ...Array.from(optionMap.entries()).map(([value, label]) => ({
        value,
        label,
      })),
    ];
  }, [rows, t]);

  const resultOptions = useMemo(
    () => [
      { value: ALL_FILTER, label: t('usage_stats.filter_all') },
      { value: RESULT_SUCCESS_FILTER, label: t('stats.success') },
      { value: RESULT_FAILURE_FILTER, label: t('stats.failure') },
    ],
    [t]
  );

  const modelOptionSet = useMemo(
    () => new Set(modelOptions.map((option) => option.value)),
    [modelOptions]
  );
  const sourceOptionSet = useMemo(
    () => new Set(sourceOptions.map((option) => option.value)),
    [sourceOptions]
  );
  const resultOptionSet = useMemo(
    () => new Set(resultOptions.map((option) => option.value)),
    [resultOptions]
  );

  const effectiveModelFilter = modelOptionSet.has(modelFilter) ? modelFilter : ALL_FILTER;
  const effectiveSourceFilter = sourceOptionSet.has(sourceFilter) ? sourceFilter : ALL_FILTER;
  const effectiveResultFilter = resultOptionSet.has(resultFilter) ? resultFilter : ALL_FILTER;

  const filteredRows = useMemo(
    () =>
      rows.filter((row) => {
        const modelMatched =
          effectiveModelFilter === ALL_FILTER || row.model === effectiveModelFilter;
        const sourceMatched =
          effectiveSourceFilter === ALL_FILTER || row.sourceKey === effectiveSourceFilter;
        const resultMatched =
          effectiveResultFilter === ALL_FILTER ||
          (effectiveResultFilter === RESULT_FAILURE_FILTER ? row.failed : !row.failed);
        return modelMatched && sourceMatched && resultMatched;
      }),
    [effectiveModelFilter, effectiveResultFilter, effectiveSourceFilter, rows]
  );

  const renderedRows = useMemo(() => filteredRows.slice(0, MAX_RENDERED_EVENTS), [filteredRows]);
  const selectableRenderedRows = useMemo(
    () => renderedRows.filter((row) => row.backendId),
    [renderedRows]
  );
  const selectedRenderedIds = useMemo(
    () => selectableRenderedRows.map((row) => row.backendId!).filter((id) => selectedRowIds[id]),
    [selectableRenderedRows, selectedRowIds]
  );
  const selectedFilteredIds = useMemo(
    () => filteredRows.map((row) => row.backendId).filter((id): id is string => Boolean(id && selectedRowIds[id])),
    [filteredRows, selectedRowIds]
  );
  const allRenderedSelected =
    selectableRenderedRows.length > 0 && selectedRenderedIds.length === selectableRenderedRows.length;
  const hasRenderedSelection = selectedRenderedIds.length > 0;

  const hasActiveFilters =
    effectiveModelFilter !== ALL_FILTER ||
    effectiveSourceFilter !== ALL_FILTER ||
    effectiveResultFilter !== ALL_FILTER;

  const handleClearFilters = () => {
    setModelFilter(ALL_FILTER);
    setSourceFilter(ALL_FILTER);
    setResultFilter(ALL_FILTER);
  };

  useEffect(() => {
    const validIds = new Set(rows.map((row) => row.backendId).filter(Boolean));
    setSelectedRowIds((current) => {
      let changed = false;
      const next: Record<string, true> = {};
      Object.keys(current).forEach((id) => {
        if (validIds.has(id)) {
          next[id] = true;
        } else {
          changed = true;
        }
      });
      return changed ? next : current;
    });
  }, [rows]);

  const toggleSelectAllRendered = useCallback(() => {
    setSelectedRowIds((current) => {
      const next = { ...current };
      if (allRenderedSelected) {
        selectableRenderedRows.forEach((row) => {
          delete next[row.backendId!];
        });
      } else {
        selectableRenderedRows.forEach((row) => {
          next[row.backendId!] = true;
        });
      }
      return next;
    });
  }, [allRenderedSelected, selectableRenderedRows]);

  const toggleSelectRow = useCallback((row: RequestEventRow) => {
    const id = row.backendId;
    if (!id) return;
    setSelectedRowIds((current) => {
      const next = { ...current };
      if (next[id]) {
        delete next[id];
      } else {
        next[id] = true;
      }
      return next;
    });
  }, []);

  const handleExportCsv = () => {
    if (!filteredRows.length) return;

    const csvHeader = [
      'timestamp',
      'model',
      'source',
      'source_raw',
      'result',
      ...(hasTimingData ? ['first_byte_latency_ms', 'generation_ms', 'tps'] : []),
      'thinking_effort',
      'input_tokens',
      'output_tokens',
      'reasoning_tokens',
      'cached_tokens',
      'total_tokens',
      'cache_hit_ratio',
    ];

    const csvRows = filteredRows.map((row) =>
      [
        row.timestamp,
        row.model,
        row.source,
        row.sourceRaw,
        row.failed ? 'failed' : 'success',
        ...(hasTimingData
          ? [
              row.firstByteLatencyMs ?? '',
              row.generationMs ?? '',
              row.tps !== null ? row.tps.toFixed(2) : '',
            ]
          : []),
        row.thinkingLabel === '-' ? '' : row.thinkingLabel,
        row.inputTokens,
        row.outputTokens,
        row.reasoningTokens,
        row.cachedTokens,
        row.totalTokens,
        row.cacheHitRatio !== null ? row.cacheHitRatio.toFixed(4) : '',
      ]
        .map((value) => encodeCsv(value))
        .join(',')
    );

    const content = [csvHeader.join(','), ...csvRows].join('\n');
    const fileTime = new Date().toISOString().replace(/[:.]/g, '-');
    downloadBlob({
      filename: `usage-events-${fileTime}.csv`,
      blob: new Blob([content], { type: 'text/csv;charset=utf-8' }),
    });
  };

  const handleExportJson = () => {
    if (!filteredRows.length) return;

    const payload = filteredRows.map((row) => ({
      timestamp: row.timestamp,
      model: row.model,
      source: row.source,
      source_raw: row.sourceRaw,
      failed: row.failed,
      ...(hasTimingData && row.firstByteLatencyMs !== null
        ? { first_byte_latency_ms: row.firstByteLatencyMs }
        : {}),
      ...(hasTimingData && row.generationMs !== null ? { generation_ms: row.generationMs } : {}),
      ...(hasTimingData && row.tps !== null ? { tps: row.tps } : {}),
      ...(row.thinkingLabel !== '-' ? { thinking_effort: row.thinkingLabel } : {}),
      tokens: {
        input_tokens: row.inputTokens,
        output_tokens: row.outputTokens,
        reasoning_tokens: row.reasoningTokens,
        cached_tokens: row.cachedTokens,
        total_tokens: row.totalTokens,
      },
      ...(row.cacheHitRatio !== null ? { cache_hit_ratio: row.cacheHitRatio } : {}),
    }));

    const content = JSON.stringify(payload, null, 2);
    const fileTime = new Date().toISOString().replace(/[:.]/g, '-');
    downloadBlob({
      filename: `usage-events-${fileTime}.json`,
      blob: new Blob([content], { type: 'application/json;charset=utf-8' }),
    });
  };

  const handleDeleteRow = useCallback(
    (row: RequestEventRow) => {
      const backendId = row.backendId;
      if (!backendId) return;
      showConfirmation({
        title: t('usage_stats.request_events_delete_title'),
        message: t('usage_stats.request_events_delete_confirm'),
        confirmText: t('common.confirm'),
        variant: 'danger',
        onConfirm: async () => {
          setDeletingId(backendId);
          try {
            await deleteUsageRecords([backendId]);
            await onRefresh?.();
            showNotification(t('usage_stats.request_events_delete_success'), 'success');
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : '';
            showNotification(
              `${t('usage_stats.request_events_delete_failed')}${message ? `: ${message}` : ''}`,
              'error'
            );
            throw err;
          } finally {
            setDeletingId(null);
          }
        },
      });
    },
    [deleteUsageRecords, onRefresh, showConfirmation, showNotification, t]
  );

  const deleteRowsByIds = useCallback(
    (ids: string[], confirmMessage: string) => {
      const uniqueIds = Array.from(new Set(ids.map((id) => id.trim()).filter(Boolean)));
      if (!uniqueIds.length) return;
      showConfirmation({
        title: '删除请求事件',
        message: confirmMessage,
        confirmText: t('common.confirm'),
        variant: 'danger',
        onConfirm: async () => {
          try {
            await deleteUsageRecords(uniqueIds);
            await onRefresh?.();
            setSelectedRowIds((current) => {
              const next = { ...current };
              uniqueIds.forEach((id) => {
                delete next[id];
              });
              return next;
            });
            showNotification(`已删除 ${uniqueIds.length} 条请求事件`, 'success');
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : '';
            showNotification(`删除请求事件失败${message ? `: ${message}` : ''}`, 'error');
            throw err;
          }
        },
      });
    },
    [deleteUsageRecords, onRefresh, showConfirmation, showNotification, t]
  );

  const handleDeleteSelectedRows = useCallback(() => {
    deleteRowsByIds(selectedFilteredIds, `确定删除已勾选的 ${selectedFilteredIds.length} 条请求事件吗？`);
  }, [deleteRowsByIds, selectedFilteredIds]);

  const handleDeleteCurrentPageRows = useCallback(() => {
    const ids = renderedRows.map((row) => row.backendId).filter((id): id is string => Boolean(id));
    deleteRowsByIds(ids, `确定删除当前页显示的 ${ids.length} 条请求事件吗？`);
  }, [deleteRowsByIds, renderedRows]);

  const handleDeleteAllRows = useCallback(() => {
    showConfirmation({
      title: '删除全部请求事件',
      message: '确定删除所有请求事件吗？该操作会清空已持久化的 usage 记录。',
      confirmText: t('common.confirm'),
      variant: 'danger',
      onConfirm: async () => {
        try {
          await deleteAllUsageRecords();
          setSelectedRowIds({});
          showNotification('已删除所有请求事件', 'success');
          await onRefresh?.();
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : '';
          showNotification(`删除所有请求事件失败${message ? `: ${message}` : ''}`, 'error');
          throw err;
        }
      },
    });
  }, [deleteAllUsageRecords, onRefresh, showConfirmation, showNotification, t]);

  const selectedCredentialInfo = useMemo(() => {
    if (!selectedFailureRow) return null;
    const normalizedAuthIndex = normalizeAuthIndex(selectedFailureRow.authIndex);
    if (!normalizedAuthIndex) return null;
    return authFileMap.get(normalizedAuthIndex) ?? null;
  }, [authFileMap, selectedFailureRow]);
  const selectedFailureCapturedRequest = useMemo(() => {
    if (!selectedFailureRow) return '';
    if (selectedFailureRow.rawRequest?.trim()) {
      return selectedFailureRow.rawRequest;
    }
    if (selectedFailureLogText) {
      const fromLog = extractLogSection(selectedFailureLogText, '=== API REQUEST ===', '=== API RESPONSE ===');
      if (fromLog) {
        return fromLog;
      }
    }
    return '';
  }, [selectedFailureLogText, selectedFailureRow]);
  const selectedFailureCapturedResponse = useMemo(() => {
    if (!selectedFailureRow) return '';
    if (selectedFailureRow.rawResponse?.trim()) {
      return selectedFailureRow.rawResponse;
    }
    if (selectedFailureLogText) {
      const fromLog = extractLogSection(selectedFailureLogText, '=== API RESPONSE ===');
      if (fromLog) {
        return fromLog;
      }
    }
    return '';
  }, [selectedFailureLogText, selectedFailureRow]);
  const selectedFailureMessage = useMemo(() => {
    if (!selectedFailureRow) return '';
    const upstreamResponse =
      extractNamedSection(selectedFailureCapturedResponse, '供应商返回CPA的完整数据包') ||
      selectedFailureCapturedResponse;
    const status = parseHTTPStatusCode(upstreamResponse, selectedFailureRow.failureStatusCode ?? null);
    if (status) {
      return `供应商响应${status}`;
    }
    const directFailure = selectedFailureRow.failureMessage?.trim();
    const directFailureSummary = summarizeFailureReason(
      directFailure || '',
      selectedFailureRow.failureStatusCode ?? null
    );
    if (directFailureSummary) {
      return directFailureSummary;
    }
    const responseBody = extractPacketBody(upstreamResponse);
    const responseMessage =
      extractFailureMessageFromJson(responseBody) ||
      extractFailureMessageFromJson(upstreamResponse) ||
      extractFailureLine(upstreamResponse) ||
      responseBody ||
      upstreamResponse.trim();
    const responseSummary = summarizeFailureReason(
      responseMessage,
      selectedFailureRow.failureStatusCode ?? null
    );
    if (responseSummary) {
      return responseSummary.length > 2000 ? `${responseSummary.slice(0, 2000)}...` : responseSummary;
    }
    return summarizeMissingFailureReason(selectedFailureRow, selectedCredentialInfo ?? undefined);
  }, [selectedCredentialInfo, selectedFailureCapturedResponse, selectedFailureRow]);
  useEffect(() => {
    let cancelled = false;
    if (!selectedFailureRow?.requestId) {
      setSelectedFailureLogText('');
      setSelectedFailureLogLoading(false);
      return;
    }
    if (selectedFailureRow.rawRequest?.trim() && selectedFailureRow.rawResponse?.trim()) {
      setSelectedFailureLogText('');
      setSelectedFailureLogLoading(false);
      return;
    }
    setSelectedFailureLogLoading(true);
    setSelectedFailureLogText('');
    void logsApi
      .downloadRequestLogById(selectedFailureRow.requestId)
      .then(async (response) => {
        const blob = response.data as Blob;
        const text = await blob.text();
        if (!cancelled) {
          setSelectedFailureLogText(text);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSelectedFailureLogText('');
        }
      })
      .finally(() => {
        if (!cancelled) {
          setSelectedFailureLogLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [selectedFailureRow]);
  const selectedFailureRequestPacket = useMemo(() => {
    if (!selectedFailureRow) return '';
    const client = extractNamedSection(selectedFailureCapturedRequest, '客户端发给CPA的完整数据包');
    if (client) return client;
    if (selectedFailureCapturedRequest) return selectedFailureCapturedRequest;
    return buildFallbackRequestPacket(selectedFailureRow);
  }, [selectedFailureCapturedRequest, selectedFailureRow]);
  const selectedFailureUpstreamRequestPacket = useMemo(() => {
    if (!selectedFailureRow) return '';
    return extractNamedSection(selectedFailureCapturedRequest, 'CPA发给供应商的完整数据包');
  }, [selectedFailureCapturedRequest, selectedFailureRow]);
  const selectedFailureUpstreamResponsePacket = useMemo(() => {
    if (!selectedFailureRow) return '';
    return firstNonEmpty(
      extractNamedSection(selectedFailureCapturedResponse, '供应商返回CPA的完整数据包'),
      selectedFailureCapturedResponse
    );
  }, [selectedFailureCapturedResponse, selectedFailureRow]);
  const selectedFailureStatusRulers = useMemo(() => {
    if (!selectedFailureRow) return '';
    return extractNamedSection(selectedFailureCapturedResponse, '触发status-rulers');
  }, [selectedFailureCapturedResponse, selectedFailureRow]);
  const selectedFailureResponsePacket = useMemo(() => {
    if (!selectedFailureRow) return '';
    const client = extractNamedSection(selectedFailureCapturedResponse, 'CPA发送给客户端的完整数据包');
    if (client) return client;
    if (selectedFailureCapturedResponse) return selectedFailureCapturedResponse;
    return buildFallbackResponsePacket(selectedFailureRow, selectedFailureMessage);
  }, [selectedFailureCapturedResponse, selectedFailureMessage, selectedFailureRow]);
  const selectedFailureNote = useMemo(() => {
    if (
      selectedFailureCapturedRequest ||
      selectedFailureCapturedResponse ||
      selectedFailureUpstreamRequestPacket ||
      selectedFailureUpstreamResponsePacket
    ) {
      return '以下内容为该失败事件捕获到的原始请求/响应数据。';
    }
    if (selectedCredentialInfo?.statusMessage?.trim()) {
      return '该事件未捕获到完整原始包，失败说明已回退到请求记录与当前凭证状态。';
    }
    return '该事件未捕获到完整原始包，失败说明为当前可还原的最接近信息。';
  }, [
    selectedCredentialInfo,
    selectedFailureCapturedRequest,
    selectedFailureCapturedResponse,
    selectedFailureUpstreamRequestPacket,
    selectedFailureUpstreamResponsePacket,
  ]);

  return (
    <Card
      title={t('usage_stats.request_events_title')}
      className={fixedHeight ? styles.requestEventsFixedCard : undefined}
      extra={
        <div className={styles.requestEventsActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleDeleteSelectedRows}
            disabled={selectedFilteredIds.length === 0}
          >
            删除勾选条目
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleDeleteCurrentPageRows}
            disabled={renderedRows.length === 0}
          >
            删除当前页条目
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleDeleteAllRows}
            disabled={rows.length === 0}
          >
            删除所有条目
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={handleClearFilters}
            disabled={!hasActiveFilters}
          >
            {t('usage_stats.clear_filters')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleExportCsv}
            disabled={filteredRows.length === 0}
          >
            {t('usage_stats.export_csv')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleExportJson}
            disabled={filteredRows.length === 0}
          >
            {t('usage_stats.export_json')}
          </Button>
        </div>
      }
    >
      <div className={styles.requestEventsToolbar}>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_model')}
          </span>
          <Select
            value={effectiveModelFilter}
            options={modelOptions}
            onChange={setModelFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_model')}
            fullWidth={false}
          />
        </div>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_source')}
          </span>
          <Select
            value={effectiveSourceFilter}
            options={sourceOptions}
            onChange={setSourceFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_source')}
            fullWidth={false}
          />
        </div>
        <div className={styles.requestEventsFilterItem}>
          <span className={styles.requestEventsFilterLabel}>
            {t('usage_stats.request_events_filter_result')}
          </span>
          <Select
            value={effectiveResultFilter}
            options={resultOptions}
            onChange={setResultFilter}
            className={styles.requestEventsSelect}
            ariaLabel={t('usage_stats.request_events_filter_result')}
            fullWidth={false}
          />
        </div>
        {onRefresh && (
          <div className={styles.requestEventsFilterItem}>
            <span className={styles.requestEventsFilterLabelRow}>
              <span className={styles.requestEventsFilterLabel}>{t('monitoring_center.auto_refresh')}</span>
              {autoRefreshCountdown !== null && (
                <span className={styles.requestEventsCountdown}>
                  {t('monitoring_center.auto_refresh_countdown', { count: autoRefreshCountdown })}
                </span>
              )}
            </span>
            <div className={styles.requestEventsAutoRefreshControls}>
              <Select
                value={autoRefreshValue}
                options={autoRefreshOptions}
                onChange={(value) => setAutoRefreshValue(value as AutoRefreshValue)}
                className={styles.requestEventsSelect}
                ariaLabel={t('monitoring_center.auto_refresh')}
                fullWidth={false}
              />
              {autoRefreshValue === AUTO_REFRESH_CUSTOM && (
                <Input
                  type="text"
                  inputMode="numeric"
                  value={customAutoRefreshSeconds}
                  onChange={(event) => handleCustomAutoRefreshSecondsChange(event.target.value)}
                  onBlur={handleCustomAutoRefreshSecondsBlur}
                  className={styles.requestEventsAutoRefreshInput}
                  aria-label={t('monitoring_center.auto_refresh_custom_seconds')}
                  placeholder={normalizedCustomAutoRefreshSeconds.toString()}
                />
              )}
            </div>
          </div>
        )}
      </div>

      {loading && rows.length === 0 ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <EmptyState
          title={t('usage_stats.request_events_empty_title')}
          description={t('usage_stats.request_events_empty_desc')}
        />
      ) : filteredRows.length === 0 ? (
        <EmptyState
          title={t('usage_stats.request_events_no_result_title')}
          description={t('usage_stats.request_events_no_result_desc')}
        />
      ) : (
        <>
          <div className={styles.requestEventsMeta}>
            <span>{t('usage_stats.request_events_count', { count: filteredRows.length })}</span>
            {filteredRows.length > MAX_RENDERED_EVENTS && (
              <span className={styles.requestEventsLimitHint}>
                {t('usage_stats.request_events_limit_hint', {
                  shown: MAX_RENDERED_EVENTS,
                })}
              </span>
            )}
          </div>

          <div className={styles.requestEventsTableWrapper}>
            <table className={`${styles.table} ${styles.requestEventsTable}`}>
              <colgroup>
                <col className={styles.requestEventsSelectCol} />
                <col className={styles.requestEventsActionCol} />
                <col className={styles.requestEventsTimestampCol} />
                <col className={styles.requestEventsModelCol} />
                <col className={styles.requestEventsSourceCol} />
                <col className={styles.requestEventsResultCol} />
                {hasTimingData && <col className={styles.requestEventsTimingCol} />}
                {hasTimingData && <col className={styles.requestEventsTimingCol} />}
                {hasTimingData && <col className={styles.requestEventsTimingCol} />}
                <col className={styles.requestEventsThinkingCol} />
                <col className={styles.requestEventsTokenCol} />
                <col className={styles.requestEventsTokenCol} />
                <col className={styles.requestEventsTokenCol} />
                <col className={styles.requestEventsTokenCol} />
                <col className={styles.requestEventsTokenCol} />
                <col className={styles.requestEventsTokenCol} />
              </colgroup>
              <thead>
                <tr>
                  <th className={styles.requestEventsSelectCell}>
                    <input
                      type="checkbox"
                      checked={allRenderedSelected}
                      ref={(element) => {
                        if (element) {
                          element.indeterminate = hasRenderedSelection && !allRenderedSelected;
                        }
                      }}
                      onChange={toggleSelectAllRendered}
                      aria-label="全选或反选当前页请求事件"
                    />
                  </th>
                  <th aria-label={t('usage_stats.request_events_delete_action')} />
                  <th>{t('usage_stats.request_events_timestamp')}</th>
                  <th>{t('usage_stats.model_name')}</th>
                  <th>{t('usage_stats.request_events_source')}</th>
                  <th>{t('usage_stats.request_events_result')}</th>
                  {hasTimingData && <th>{t('usage_stats.first_byte_latency')}</th>}
                  {hasTimingData && <th>{t('usage_stats.generation_time')}</th>}
                  {hasTimingData && <th>{t('usage_stats.request_events_tps')}</th>}
                  <th>{t('usage_stats.thinking_intensity')}</th>
                  <th>{t('usage_stats.input_tokens')}</th>
                  <th>{t('usage_stats.output_tokens')}</th>
                  <th>{t('usage_stats.reasoning_tokens')}</th>
                  <th>{t('usage_stats.cached_tokens')}</th>
                  <th>{t('usage_stats.total_tokens')}</th>
                  <th>{t('usage_stats.cache_hit')}</th>
                </tr>
              </thead>
              <tbody>
                {renderedRows.map((row) => (
                  <tr key={row.id}>
                    <td className={styles.requestEventsSelectCell}>
                      <input
                        type="checkbox"
                        checked={Boolean(row.backendId && selectedRowIds[row.backendId])}
                        disabled={!row.backendId}
                        onChange={() => toggleSelectRow(row)}
                        aria-label="勾选请求事件"
                      />
                    </td>
                    <td className={styles.requestEventsDeleteCell}>
                      <button
                        type="button"
                        className={styles.requestEventsDeleteButton}
                        onClick={() => handleDeleteRow(row)}
                        disabled={!row.backendId || deletingId === row.backendId}
                        title={t('usage_stats.request_events_delete_action')}
                        aria-label={t('usage_stats.request_events_delete_action')}
                      >
                        <IconMinus size={14} />
                      </button>
                    </td>
                    <td title={row.timestamp} className={styles.requestEventsTimestamp}>
                      {row.timestampLabel}
                    </td>
                    <td className={styles.modelCell}>{row.model}</td>
                    <td className={styles.requestEventsSourceCell} title={row.source}>
                      <span>{row.source}</span>
                      {row.sourceType && (
                        <span className={styles.credentialType}>{row.sourceType}</span>
                      )}
                    </td>
                    <td>
                      {row.failed ? (
                        <button
                          type="button"
                          className={`${styles.requestEventsResultFailed} ${styles.requestEventsResultButton}`}
                          onClick={() => setSelectedFailureRow(row)}
                          aria-label={t('usage_stats.request_events_failure_log_view')}
                        >
                          {formatResultLabel(row)}
                        </button>
                      ) : (
                        <span className={styles.requestEventsResultSuccess}>{formatResultLabel(row)}</span>
                      )}
                    </td>
                    {hasTimingData && (
                      <td className={styles.durationCell}>{formatDurationMs(row.firstByteLatencyMs)}</td>
                    )}
                    {hasTimingData && (
                      <td className={styles.durationCell}>{formatDurationMs(row.generationMs)}</td>
                    )}
                    {hasTimingData && <td>{row.tps !== null ? row.tps.toFixed(2) : '--'}</td>}
                    <td>
                      <span
                        className={
                          row.thinkingLabel !== '-'
                            ? styles.requestEventsThinkingBadge
                            : styles.requestEventsThinkingEmpty
                        }
                        title={
                          row.thinking
                            ? [
                                row.thinking.mode
                                  ? `${t('usage_stats.thinking_mode')}: ${row.thinking.mode}`
                                  : '',
                                row.thinking.level
                                  ? `${t('usage_stats.thinking_level')}: ${row.thinking.level}`
                                  : '',
                                typeof row.thinking.budget === 'number'
                                  ? `${t('usage_stats.thinking_budget')}: ${row.thinking.budget.toLocaleString()}`
                                  : '',
                              ]
                                .filter(Boolean)
                                .join(' · ')
                            : undefined
                        }
                      >
                        {row.thinkingLabel}
                      </span>
                    </td>
                    <td>{row.inputTokens.toLocaleString()}</td>
                    <td>{row.outputTokens.toLocaleString()}</td>
                    <td>{row.reasoningTokens.toLocaleString()}</td>
                    <td>{row.cachedTokens.toLocaleString()}</td>
                    <td>{row.totalTokens.toLocaleString()}</td>
                    <td>{formatCacheHitRatio(row.cacheHitRatio)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      <Modal
        open={selectedFailureRow !== null}
        title={t('usage_stats.request_events_failure_log_title')}
        onClose={() => setSelectedFailureRow(null)}
        width={560}
      >
        {selectedFailureRow && (
          <div className={styles.requestEventsFailureModalBody}>
            <div className={styles.requestEventsFailureMeta}>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>
                  {t('usage_stats.request_events_failure_log_timestamp')}
                </span>
                <span className={styles.requestEventsFailureMetaValue}>
                  {selectedFailureRow.timestampLabel}
                </span>
              </div>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>
                  {t('usage_stats.request_events_failure_log_model')}
                </span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedFailureRow.model}</span>
              </div>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>Endpoint</span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedFailureRow.endpoint}</span>
              </div>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>Request ID</span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedFailureRow.requestId ?? '-'}</span>
              </div>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>Provider</span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedFailureRow.provider}</span>
              </div>
              <div>
                <span className={styles.requestEventsFailureMetaLabel}>Source</span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedFailureRow.source}</span>
              </div>
            </div>

            {selectedCredentialInfo?.name && (
              <div className={styles.requestEventsFailureCredentialRow}>
                <span className={styles.requestEventsFailureMetaLabel}>
                  {t('usage_stats.request_events_failure_log_credential')}
                </span>
                <span className={styles.requestEventsFailureMetaValue}>{selectedCredentialInfo.name}</span>
              </div>
            )}

            <div className={styles.requestEventsFailureMessageBlock}>
              <div className={styles.requestEventsFailureMetaLabel}>
                {t('usage_stats.request_events_failure_log_message_label')}
              </div>
              <div className={styles.requestEventsFailureMessage}>
                {selectedFailureMessage || t('usage_stats.request_events_failure_log_empty')}
              </div>
            </div>

            {selectedFailureLogLoading ? (
              <div className={styles.requestEventsFailureNote}>正在补充读取完整请求日志…</div>
            ) : null}

            <div className={styles.requestEventsFailureMessageBlock}>
              <div className={styles.requestEventsFailureMetaLabel}>客户端发给CPA的完整数据包</div>
              <pre className={styles.requestEventsFailurePacket}>
                {selectedFailureRequestPacket}
              </pre>
            </div>

            {selectedFailureUpstreamRequestPacket && (
              <div className={styles.requestEventsFailureMessageBlock}>
                <div className={styles.requestEventsFailureMetaLabel}>CPA发给供应商的完整数据包</div>
                <pre className={styles.requestEventsFailurePacket}>
                  {selectedFailureUpstreamRequestPacket}
                </pre>
              </div>
            )}

            <div className={styles.requestEventsFailureMessageBlock}>
              <div className={styles.requestEventsFailureMetaLabel}>供应商返回CPA的完整数据包</div>
              <pre className={styles.requestEventsFailurePacket}>
                {selectedFailureUpstreamResponsePacket || selectedFailureResponsePacket}
              </pre>
            </div>

            {selectedFailureStatusRulers && (
              <div className={styles.requestEventsFailureMessageBlock}>
                <div className={styles.requestEventsFailureMetaLabel}>触发status-rulers</div>
                <pre className={styles.requestEventsFailurePacket}>
                  {selectedFailureStatusRulers}
                </pre>
              </div>
            )}

            <div className={styles.requestEventsFailureMessageBlock}>
              <div className={styles.requestEventsFailureMetaLabel}>CPA发送给客户端的完整数据包</div>
              <pre className={styles.requestEventsFailurePacket}>
                {selectedFailureResponsePacket}
              </pre>
            </div>

            <div className={styles.requestEventsFailureNote}>
              {selectedFailureNote}
            </div>
          </div>
        )}
      </Modal>
    </Card>
  );
}
