/**
 * Generic hook for quota data fetching and management.
 */

import { useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthFileItem } from '@/types';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from '@/stores';
import { getStatusFromError } from '@/utils/quota';
import type { QuotaConfig } from './quotaConfigs';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaSetter<T> = (updater: QuotaUpdater<T>) => void;

interface LoadQuotaResult<TData> {
  name: string;
  status: 'success' | 'error';
  data?: TData;
  error?: string;
  errorStatus?: number;
}

const QUOTA_REFRESH_CONCURRENCY = 4;

export function useQuotaLoader<TState, TData>(config: QuotaConfig<TState, TData>) {
  const { t } = useTranslation();
  const quota = useQuotaStore(config.storeSelector);
  const setQuota = useQuotaStore((state) => state[config.storeSetter]) as QuotaSetter<
    Record<string, TState>
  >;

  const loadingRef = useRef(false);
  const requestIdRef = useRef(0);

  const loadQuota = useCallback(
    async (targets: AuthFileItem[], setLoading: (loading: boolean) => void) => {
      if (loadingRef.current) return;
      loadingRef.current = true;
      const requestId = ++requestIdRef.current;
      const cacheGeneration = captureQuotaCacheGeneration();
      setLoading(true);

      try {
        if (targets.length === 0) return;

        setQuota((prev) => {
          const nextState = { ...prev };
          targets.forEach((file) => {
            nextState[file.name] = config.buildLoadingState();
          });
          return nextState;
        });

        const results: LoadQuotaResult<TData>[] = [];
        let nextIndex = 0;
        const runNext = async (): Promise<void> => {
          while (nextIndex < targets.length) {
            const currentIndex = nextIndex;
            nextIndex += 1;
            const file = targets[currentIndex];
            if (!file) continue;

            try {
              const data = await config.fetchQuota(file, t);
              results[currentIndex] = { name: file.name, status: 'success', data };
            } catch (err: unknown) {
              const message = err instanceof Error ? err.message : t('common.unknown_error');
              const errorStatus = getStatusFromError(err);
              results[currentIndex] = {
                name: file.name,
                status: 'error',
                error: message,
                errorStatus,
              };
            }
          }
        };
        const workers = Array.from(
          { length: Math.min(QUOTA_REFRESH_CONCURRENCY, targets.length) },
          () => runNext()
        );
        await Promise.all(workers);

        if (requestId !== requestIdRef.current) return;

        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setQuota((prev) => {
            const nextState = { ...prev };
            results.forEach((result) => {
              if (result.status === 'success') {
                nextState[result.name] = config.buildSuccessState(result.data as TData);
              } else {
                nextState[result.name] = config.buildErrorState(
                  result.error || t('common.unknown_error'),
                  result.errorStatus
                );
              }
            });
            return nextState;
          });
        });
      } finally {
        if (requestId === requestIdRef.current) {
          setLoading(false);
          loadingRef.current = false;
        }
      }
    },
    [config, setQuota, t]
  );

  return { quota, loadQuota };
}
