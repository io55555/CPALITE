import { apiClient } from './client';

export type RuntimeMetrics = {
  timestamp?: string;
  process?: Record<string, number | string>;
  system?: Record<string, number>;
  auth?: Record<string, number>;
  auth_index?: Record<string, number | string | boolean>;
};

export const runtimeMetricsApi = {
  get: () => apiClient.get<RuntimeMetrics>('/system/runtime'),
};
