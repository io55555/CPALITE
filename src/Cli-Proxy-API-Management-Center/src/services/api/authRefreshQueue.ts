import { apiClient } from './client';
import type { AuthRefreshQueueResponse } from '@/types/authRefreshQueue';

export const authRefreshQueueApi = {
  list: () => apiClient.get<AuthRefreshQueueResponse>('/auth-refresh-queue')
};
