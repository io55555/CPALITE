/**
 * 版本相关 API
 */

import { apiClient } from './client';
import type { ServerRuntimeKind } from '@/types';
import { isRecord } from '@/utils/helpers';

export const versionApi = {
  checkLatest: () => apiClient.get<Record<string, unknown>>('/latest-version'),

  // 通过 /nodes 探测运行时：Home 返回 nodes，CPA 通常 404/405
  async detectRuntimeKind(): Promise<ServerRuntimeKind> {
    try {
      const data = await apiClient.get('/nodes');
      return isRecord(data) && Array.isArray(data.nodes) ? 'home' : 'unknown';
    } catch (error: unknown) {
      const status = isRecord(error) ? error.status : undefined;
      if (status === 404 || status === 405) {
        return 'cpa';
      }
      return 'unknown';
    }
  },
};
