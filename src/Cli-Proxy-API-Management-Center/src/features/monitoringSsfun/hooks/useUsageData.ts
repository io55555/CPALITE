import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import { apiClient } from '@/services/api/client';
import { useAuthStore } from '@/stores/useAuthStore';
import { computeApiUrl } from '@/utils/connection';
import { isRecordValue } from '@/utils/quota';
import {
  loadLegacyModelPrices,
  loadModelPricesFromSqlite,
  saveModelPricesToSqlite,
  type ModelPrice,
} from '@/utils/usageSsfun';

export interface UsagePayload {
  total_requests?: number;
  success_count?: number;
  failure_count?: number;
  total_tokens?: number;
  latest_id?: number;
  apis?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface UseUsageDataReturn {
  usage: UsagePayload | null;
  loading: boolean;
  error: string;
  lastRefreshedAt: Date | null;
  modelPrices: Record<string, ModelPrice>;
  setModelPrices: (prices: Record<string, ModelPrice>) => void;
  refreshUsage: () => Promise<void>;
}

const toNumber = (value: unknown) => (Number.isFinite(Number(value)) ? Number(value) : 0);

type UsageModelEntry = { details?: unknown[]; [key: string]: unknown };
type UsageApiEntry = { models?: Record<string, UsageModelEntry>; [key: string]: unknown };

const asUsageApiEntry = (value: unknown): UsageApiEntry =>
  isRecordValue(value) ? (value as UsageApiEntry) : {};

const mergeUsagePayload = (current: UsagePayload | null, next: UsagePayload | null): UsagePayload | null => {
  if (!next) return current;
  if (!current) return next;

  const currentLatestId = toNumber(current.latest_id);
  const nextLatestId = toNumber(next.latest_id);
  if (nextLatestId <= currentLatestId) return current;

  let mergedApis = current.apis;
  Object.entries(next.apis ?? {}).forEach(([endpoint, apiEntry]) => {
    const existingApi = asUsageApiEntry(current.apis?.[endpoint]);
    const nextApi = asUsageApiEntry(apiEntry);
    const models: Record<string, UsageModelEntry> = { ...(existingApi.models ?? {}) };

    Object.entries(nextApi.models ?? {}).forEach(([model, modelEntry]) => {
      const existingModel = models[model];
      models[model] = {
        ...(existingModel ?? {}),
        ...(modelEntry ?? {}),
        details: [
          ...(Array.isArray(existingModel?.details) ? existingModel.details : []),
          ...(Array.isArray(modelEntry?.details) ? modelEntry.details : []),
        ],
      };
    });

    const writableApis: Record<string, unknown> = mergedApis === current.apis ? { ...(current.apis ?? {}) } : (mergedApis ?? {});
    mergedApis = writableApis;
    writableApis[endpoint] = {
      ...existingApi,
      ...nextApi,
      models,
    };
  });

  return {
    ...current,
    total_requests: toNumber(current.total_requests) + toNumber(next.total_requests),
    success_count: toNumber(current.success_count) + toNumber(next.success_count),
    failure_count: toNumber(current.failure_count) + toNumber(next.failure_count),
    total_tokens: toNumber(current.total_tokens) + toNumber(next.total_tokens),
    latest_id: nextLatestId,
    apis: mergedApis,
  };
};

const buildUsageStreamUrl = (apiBase: string, afterId: number) => {
  const base = computeApiUrl(apiBase);
  if (!base) return '';
  const url = new URL(`${base}/usage/stream`);
  url.searchParams.set('after_id', String(Math.max(afterId, 0)));
  return url.toString();
};

const readSseMessage = (block: string): { event: string; data: string } | null => {
  if (!block.trim()) return null;
  let event = 'message';
  const dataLines: string[] = [];
  block.split('\n').forEach((line) => {
    if (line.startsWith('event:')) event = line.slice(6).trim();
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  });
  return dataLines.length > 0 ? { event, data: dataLines.join('\n') } : null;
};

const parseUsageSsePayload = (block: string): UsagePayload | null => {
  const message = readSseMessage(block);
  if (message?.event !== 'usage') return null;
  return JSON.parse(message.data) as UsagePayload;
};

const nextUsageReconnectDelay = (currentDelay: number) => Math.min(currentDelay * 2, 30000);

type MutableRef<T> = { current: T };

type UsageStateWriter = {
  setUsage: Dispatch<SetStateAction<UsagePayload | null>>;
  setLoading: (loading: boolean) => void;
  setError: (error: string) => void;
  setLastRefreshedAt: (date: Date | null) => void;
};

const loadUsageSnapshot = async ({
  requestIdRef,
  latestIdRef,
  setUsage,
  setLoading,
  setError,
  setLastRefreshedAt,
}: UsageStateWriter & {
  requestIdRef: MutableRef<number>;
  latestIdRef: MutableRef<number>;
}) => {
  const requestId = requestIdRef.current + 1;
  requestIdRef.current = requestId;
  setLoading(true);
  setError('');

  try {
    const payload = await apiClient.get<UsagePayload>('/usage');
    if (requestIdRef.current !== requestId) return;
    latestIdRef.current = toNumber(payload?.latest_id);
    setUsage(payload ?? null);
    setLastRefreshedAt(new Date());
  } catch (err) {
    if (requestIdRef.current !== requestId) return;
    setError(err instanceof Error ? err.message : String(err));
  } finally {
    if (requestIdRef.current === requestId) {
      setLoading(false);
    }
  }
};

const loadUsageIncrementalSnapshot = async ({
  latestIdRef,
  incrementalLoadingRef,
  incrementalPendingRef,
  loadUsage,
  applyUsagePayload,
}: {
  latestIdRef: MutableRef<number>;
  incrementalLoadingRef: MutableRef<boolean>;
  incrementalPendingRef: MutableRef<boolean>;
  loadUsage: () => Promise<void>;
  applyUsagePayload: (payload: UsagePayload | null) => void;
}) => {
  if (incrementalLoadingRef.current) {
    incrementalPendingRef.current = true;
    return;
  }

  incrementalLoadingRef.current = true;
  try {
    do {
      incrementalPendingRef.current = false;
      const afterId = latestIdRef.current;
      if (afterId <= 0) {
        await loadUsage();
        continue;
      }

      try {
        const payload = await apiClient.get<UsagePayload>(`/usage/events?after_id=${afterId}&limit=5000`);
        applyUsagePayload(payload ?? null);
      } catch {
        await loadUsage();
      }
    } while (incrementalPendingRef.current);
  } finally {
    incrementalLoadingRef.current = false;
  }
};

const connectUsageStream = async ({
  apiBase,
  managementKey,
  signal,
  latestIdRef,
  applyUsagePayload,
  loadUsageIncremental,
}: {
  apiBase: string;
  managementKey: string;
  signal: AbortSignal;
  latestIdRef: MutableRef<number>;
  applyUsagePayload: (payload: UsagePayload | null) => void;
  loadUsageIncremental: () => Promise<void>;
}) => {
  const decoder = new TextDecoder();
  let buffer = '';
  const url = buildUsageStreamUrl(apiBase, latestIdRef.current);
  if (!url) return;

  const response = await fetch(url, {
    headers: { Authorization: `Bearer ${managementKey}` },
    signal,
  });
  if (!response.ok || !response.body) {
    throw new Error(`用量实时流连接失败：${response.status}`);
  }

  const reader = response.body.getReader();
  while (!signal.aborted) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const parts = buffer.split('\n\n');
    buffer = parts.pop() ?? '';
    parts.forEach((part) => {
      try {
        const payload = parseUsageSsePayload(part);
        if (payload) {
          applyUsagePayload(payload);
        }
      } catch {
        void loadUsageIncremental();
      }
    });
  }
};

export function useUsageData(): UseUsageDataReturn {
  const [usage, setUsage] = useState<UsagePayload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const [modelPrices, setModelPricesState] = useState<Record<string, ModelPrice>>({});
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const requestIdRef = useRef(0);
  const latestIdRef = useRef(0);
  const incrementalLoadingRef = useRef(false);
  const incrementalPendingRef = useRef(false);

  const loadUsage = useCallback(() => loadUsageSnapshot({
    requestIdRef,
    latestIdRef,
    setUsage,
    setLoading,
    setError,
    setLastRefreshedAt,
  }), []);

  const applyUsagePayload = useCallback((payload: UsagePayload | null) => {
    const nextLatestId = toNumber(payload?.latest_id);
    if (nextLatestId <= latestIdRef.current) return;
    latestIdRef.current = nextLatestId;
    setUsage((current) => mergeUsagePayload(current, payload));
    setLastRefreshedAt(new Date());
  }, []);

  const loadUsageIncremental = useCallback(() => loadUsageIncrementalSnapshot({
    latestIdRef,
    incrementalLoadingRef,
    incrementalPendingRef,
    loadUsage,
    applyUsagePayload,
  }), [applyUsagePayload, loadUsage]);

  useEffect(() => {
    let cancelled = false;
    const legacyPrices = loadLegacyModelPrices();
    setModelPricesState(legacyPrices);

    const syncModelPrices = async () => {
      try {
        const sqlitePrices = await loadModelPricesFromSqlite();
        if (cancelled) return;
        if (Object.keys(sqlitePrices).length > 0) {
          setModelPricesState(sqlitePrices);
          return;
        }
        if (Object.keys(legacyPrices).length > 0) {
          await saveModelPricesToSqlite(legacyPrices);
        }
      } catch (err) {
        console.error('同步模型价格失败：', err);
      }
    };

    void syncModelPrices();
    void loadUsage();

    return () => {
      cancelled = true;
    };
  }, [loadUsage]);

  useEffect(() => {
    if (connectionStatus !== 'connected' || !apiBase || !managementKey) return;

    const controller = new AbortController();
    let reconnectDelay = 1000;
    let timeoutId: ReturnType<typeof setTimeout> | null = null;

    const connect = async () => {
      try {
        await connectUsageStream({
          apiBase,
          managementKey,
          signal: controller.signal,
          latestIdRef,
          applyUsagePayload,
          loadUsageIncremental,
        });
        reconnectDelay = 1000;
      } catch (err) {
        if (!controller.signal.aborted) {
          console.warn('用量实时流已断开：', err);
        }
      }

      if (!controller.signal.aborted) {
        timeoutId = setTimeout(() => {
          void loadUsageIncremental();
          void connect();
        }, reconnectDelay);
        reconnectDelay = nextUsageReconnectDelay(reconnectDelay);
      }
    };

    void connect();
    return () => {
      controller.abort();
      if (timeoutId) clearTimeout(timeoutId);
    };
  }, [apiBase, applyUsagePayload, connectionStatus, loadUsageIncremental, managementKey]);

  const setModelPrices = useCallback((prices: Record<string, ModelPrice>) => {
    setModelPricesState(prices);
    void saveModelPricesToSqlite(prices).catch((err) => {
      console.error('保存模型价格失败：', err);
    });
  }, []);

  return {
    usage,
    loading,
    error,
    lastRefreshedAt,
    modelPrices,
    setModelPrices,
    refreshUsage: loadUsageIncremental,
  };
}

