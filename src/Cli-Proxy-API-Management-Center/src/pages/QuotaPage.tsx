/**
 * Quota management page - coordinates the three quota sections.
 */

import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useAuthStore } from '@/stores';
import { authFilesApi, configFileApi } from '@/services/api';
import {
  QuotaSection,
  ANTIGRAVITY_CONFIG,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  GEMINI_CLI_CONFIG,
  KIMI_CONFIG,
  XAI_CONFIG
} from '@/components/quota';
import type { AuthFileItem } from '@/types';
import styles from './QuotaPage.module.scss';

const QUOTA_PROVIDERS = ['claude', 'antigravity', 'codex', 'gemini-cli', 'kimi', 'xai'];
const DEFAULT_QUOTA_PAGE_SIZE = 50;

type ProviderPageState = {
  files: AuthFileItem[];
  total: number;
  page: number;
  pageSize: number;
  loading: boolean;
};

type ProviderPagesState = Record<string, ProviderPageState>;

const emptyProviderPage = (pageSize = DEFAULT_QUOTA_PAGE_SIZE): ProviderPageState => ({
  files: [],
  total: 0,
  page: 1,
  pageSize,
  loading: false,
});

const summaryProviderCounts = (summary: unknown): Record<string, number> => {
  if (!summary || typeof summary !== 'object') return {};
  const record = summary as { by_provider?: unknown; byProvider?: unknown };
  const raw = record.by_provider ?? record.byProvider;
  if (!raw || typeof raw !== 'object') return {};
  return Object.entries(raw as Record<string, unknown>).reduce<Record<string, number>>(
    (result, [key, value]) => {
      const provider = key.trim().toLowerCase();
      const count = Number(value);
      if (provider && Number.isFinite(count) && count > 0) {
        result[provider] = count;
      }
      return result;
    },
    {}
  );
};

export function QuotaPage() {
  const { t } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);

  const [providerPages, setProviderPages] = useState<ProviderPagesState>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const disableControls = connectionStatus !== 'connected';

  const loadConfig = useCallback(async () => {
    try {
      await configFileApi.fetchConfigYaml();
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError((prev) => prev || errorMessage);
    }
  }, [t]);

  const loadProviderPage = useCallback(
    async (provider: string, page: number, pageSize: number) => {
      const normalizedProvider = provider.trim().toLowerCase();
      if (!normalizedProvider) return;
      setProviderPages((current) => ({
        ...current,
        [normalizedProvider]: {
          ...(current[normalizedProvider] ?? emptyProviderPage(pageSize)),
          page,
          pageSize,
          loading: true,
        },
      }));
      try {
        const payload = await authFilesApi.list({
          provider: normalizedProvider,
          page,
          pageSize,
          status: 'enabled',
        });
        setProviderPages((current) => ({
          ...current,
          [normalizedProvider]: {
            files: payload.files || [],
            total: typeof payload.total === 'number' ? payload.total : payload.files?.length ?? 0,
            page: typeof payload.page === 'number' && payload.page > 0 ? payload.page : page,
            pageSize:
              typeof payload.page_size === 'number' && payload.page_size > 0
                ? payload.page_size
                : pageSize,
            loading: false,
          },
        }));
      } catch (err: unknown) {
        const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
        setError(errorMessage);
        setProviderPages((current) => ({
          ...current,
          [normalizedProvider]: {
            ...(current[normalizedProvider] ?? emptyProviderPage(pageSize)),
            page,
            pageSize,
            loading: false,
          },
        }));
      }
    },
    [t]
  );

  const loadFiles = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const summaryPage = await authFilesApi.list({ page: 1, pageSize: 1, status: 'enabled' });
      const counts = summaryProviderCounts(summaryPage.summary);
      const providers = QUOTA_PROVIDERS.filter((provider) => counts[provider] > 0);
      setProviderPages((current) => {
        const next: ProviderPagesState = {};
        QUOTA_PROVIDERS.forEach((provider) => {
          const previous = current[provider] ?? emptyProviderPage();
          next[provider] = {
            ...previous,
            total: counts[provider] ?? 0,
            page: 1,
            loading: providers.includes(provider),
          };
        });
        return next;
      });
      const pages = await Promise.all(
        providers.map(async (provider) => {
          const payload = await authFilesApi.list({
            provider,
            page: 1,
            pageSize: DEFAULT_QUOTA_PAGE_SIZE,
            status: 'enabled',
          });
          return { provider, payload };
        })
      );
      setProviderPages((current) => {
        const next = { ...current };
        pages.forEach(({ provider, payload }) => {
          next[provider] = {
            files: payload.files || [],
            total: typeof payload.total === 'number' ? payload.total : counts[provider] ?? 0,
            page: typeof payload.page === 'number' && payload.page > 0 ? payload.page : 1,
            pageSize:
              typeof payload.page_size === 'number' && payload.page_size > 0
                ? payload.page_size
                : DEFAULT_QUOTA_PAGE_SIZE,
            loading: false,
          };
        });
        QUOTA_PROVIDERS.forEach((provider) => {
          if (providers.includes(provider)) return;
          next[provider] = {
            ...(next[provider] ?? emptyProviderPage()),
            files: [],
            total: counts[provider] ?? 0,
            page: 1,
            loading: false,
          };
        });
        return next;
      });
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(errorMessage);
    } finally {
      setLoading(false);
    }
  }, [t]);

  const handleHeaderRefresh = useCallback(async () => {
    await Promise.all([loadConfig(), loadFiles()]);
  }, [loadConfig, loadFiles]);

  const handleClaudePageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('claude', page, pageSize),
    [loadProviderPage]
  );
  const handleAntigravityPageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('antigravity', page, pageSize),
    [loadProviderPage]
  );
  const handleCodexPageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('codex', page, pageSize),
    [loadProviderPage]
  );
  const handleGeminiCliPageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('gemini-cli', page, pageSize),
    [loadProviderPage]
  );
  const handleKimiPageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('kimi', page, pageSize),
    [loadProviderPage]
  );
  const handleXaiPageChange = useCallback(
    (page: number, pageSize: number) => loadProviderPage('xai', page, pageSize),
    [loadProviderPage]
  );

  useHeaderRefresh(handleHeaderRefresh);

  useEffect(() => {
    loadFiles();
    loadConfig();
  }, [loadFiles, loadConfig]);

  return (
    <div className={styles.container}>
      <div className={styles.pageHeader}>
        <h1 className={styles.pageTitle}>{t('quota_management.title')}</h1>
        <p className={styles.description}>{t('quota_management.description')}</p>
      </div>

      {error && <div className={styles.errorBox}>{error}</div>}

      <QuotaSection
        config={CLAUDE_CONFIG}
        files={providerPages.claude?.files ?? []}
        loading={loading || providerPages.claude?.loading === true}
        disabled={disableControls}
        totalCount={providerPages.claude?.total ?? 0}
        currentPage={providerPages.claude?.page ?? 1}
        pageSize={providerPages.claude?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleClaudePageChange}
      />
      <QuotaSection
        config={ANTIGRAVITY_CONFIG}
        files={providerPages.antigravity?.files ?? []}
        loading={loading || providerPages.antigravity?.loading === true}
        disabled={disableControls}
        totalCount={providerPages.antigravity?.total ?? 0}
        currentPage={providerPages.antigravity?.page ?? 1}
        pageSize={providerPages.antigravity?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleAntigravityPageChange}
      />
      <QuotaSection
        config={CODEX_CONFIG}
        files={providerPages.codex?.files ?? []}
        loading={loading || providerPages.codex?.loading === true}
        disabled={disableControls}
        totalCount={providerPages.codex?.total ?? 0}
        currentPage={providerPages.codex?.page ?? 1}
        pageSize={providerPages.codex?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleCodexPageChange}
      />
      <QuotaSection
        config={GEMINI_CLI_CONFIG}
        files={providerPages['gemini-cli']?.files ?? []}
        loading={loading || providerPages['gemini-cli']?.loading === true}
        disabled={disableControls}
        totalCount={providerPages['gemini-cli']?.total ?? 0}
        currentPage={providerPages['gemini-cli']?.page ?? 1}
        pageSize={providerPages['gemini-cli']?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleGeminiCliPageChange}
      />
      <QuotaSection
        config={KIMI_CONFIG}
        files={providerPages.kimi?.files ?? []}
        loading={loading || providerPages.kimi?.loading === true}
        disabled={disableControls}
        totalCount={providerPages.kimi?.total ?? 0}
        currentPage={providerPages.kimi?.page ?? 1}
        pageSize={providerPages.kimi?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleKimiPageChange}
      />
      <QuotaSection
        config={XAI_CONFIG}
        files={providerPages.xai?.files ?? []}
        loading={loading || providerPages.xai?.loading === true}
        disabled={disableControls}
        totalCount={providerPages.xai?.total ?? 0}
        currentPage={providerPages.xai?.page ?? 1}
        pageSize={providerPages.xai?.pageSize ?? DEFAULT_QUOTA_PAGE_SIZE}
        onPageChange={handleXaiPageChange}
      />
    </div>
  );
}
