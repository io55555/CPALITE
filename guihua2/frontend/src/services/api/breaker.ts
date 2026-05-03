import { apiClient } from './client';

export interface BreakerState {
  scope: string;
  key: string;
  auth_id?: string;
  auth_index?: string;
  provider?: string;
  proxy_url?: string;
  status: string;
  failure_count: number;
  last_failure_at?: string;
  last_success_at?: string;
  cooldown_until?: string;
  last_error?: string;
  probe_in_flight?: boolean;
  updated_at?: string;
}

export const breakerApi = {
  list: () => apiClient.get<{ items: BreakerState[] }>('/breaker'),
  reset: (scope?: string, key?: string) => apiClient.post<{ status: string }>('/breaker/reset', { scope, key }),
};
