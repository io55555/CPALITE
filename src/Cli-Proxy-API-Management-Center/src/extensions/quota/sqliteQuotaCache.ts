import { apiClient } from '@/services/api/client';

export interface QuotaCacheEntry<T = unknown> {
  id: string;
  provider: string;
  fileName: string;
  data: T;
  cachedAt: number;
  accessedAt: number;
  version: number;
}

interface QuotaCacheListResponse<T = unknown> {
  items?: QuotaCacheEntry<T>[];
}

interface QuotaCacheStatsResponse {
  totalEntries?: number;
  updatedAt?: number;
}

class SqliteQuotaCache {
  private async fetchEntries<T = unknown>(provider?: string): Promise<QuotaCacheEntry<T>[]> {
    const response = await apiClient.get<QuotaCacheListResponse<T>>('/usage/quota-cache', {
      params: provider ? { provider } : undefined,
    });
    return response.items ?? [];
  }

  async get<T>(provider: string, fileName: string): Promise<T | null> {
    try {
      const response = await apiClient.get<QuotaCacheListResponse<T>>('/usage/quota-cache', {
        params: { provider, fileName },
      });
      return response.items?.[0]?.data ?? null;
    } catch (err) {
      console.error('SQLite quota cache get error:', err);
      return null;
    }
  }

  async batchGet(provider: string, fileNames: string[]): Promise<Map<string, unknown>> {
    const result = new Map<string, unknown>();
    if (fileNames.length === 0) return result;

    try {
      const expected = new Set(fileNames);
      const entries = await this.fetchEntries(provider);
      entries.forEach((entry) => {
        if (expected.has(entry.fileName)) {
          result.set(entry.fileName, entry.data);
        }
      });
    } catch (err) {
      console.error('SQLite quota cache batchGet error:', err);
    }
    return result;
  }

  async set(provider: string, fileName: string, data: unknown, cachedAt = Date.now()): Promise<boolean> {
    try {
      await apiClient.put('/usage/quota-cache', {
        provider,
        fileName,
        data,
        cachedAt,
        accessedAt: Date.now(),
        version: 1,
      });
      return true;
    } catch (err) {
      console.error('SQLite quota cache set error:', err);
      return false;
    }
  }

  async delete(provider: string, fileName: string): Promise<void> {
    try {
      await apiClient.delete('/usage/quota-cache', {
        params: { provider, fileName },
      });
    } catch (err) {
      console.error('SQLite quota cache delete error:', err);
    }
  }

  async clear(): Promise<void> {
    try {
      await apiClient.delete('/usage/quota-cache');
    } catch (err) {
      console.error('SQLite quota cache clear error:', err);
    }
  }

  async getAll<T = unknown>(): Promise<QuotaCacheEntry<T>[]> {
    try {
      return await this.fetchEntries<T>();
    } catch (err) {
      console.error('SQLite quota cache getAll error:', err);
      return [];
    }
  }

  async getFileNamesByProvider(provider: string): Promise<string[]> {
    try {
      const entries = await this.fetchEntries(provider);
      return entries.map((entry) => entry.fileName);
    } catch (err) {
      console.error('SQLite quota cache getFileNamesByProvider error:', err);
      return [];
    }
  }

  async getStats(): Promise<{ totalEntries: number; updatedAt: number }> {
    try {
      const stats = await apiClient.get<QuotaCacheStatsResponse>('/usage/quota-cache', { params: { stats: '1' } });
      return { totalEntries: stats.totalEntries ?? 0, updatedAt: stats.updatedAt ?? 0 };
    } catch (err) {
      console.error('SQLite quota cache getStats error:', err);
      return { totalEntries: 0, updatedAt: 0 };
    }
  }
}

export const sqliteQuotaCache = new SqliteQuotaCache();
