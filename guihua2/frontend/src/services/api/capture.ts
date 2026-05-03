import { apiClient } from './client';

export interface CaptureSettings {
  enabled: boolean;
  retention_days: number;
  max_body_bytes: number;
}

export interface CaptureRecord {
  id: number;
  created_at: string;
  request_id: string;
  method: string;
  path: string;
  status_code: number;
  success: boolean;
  duration_ms: number;
  provider?: string;
  access_provider?: string;
  auth_id?: string;
  auth_index?: string;
  token?: string;
  api_key?: string;
  proxy_url?: string;
  error_text?: string;
  request_headers?: string;
  request_body?: string;
  upstream_request_url?: string;
  upstream_request_headers?: string;
  upstream_request_body?: string;
  upstream_status_code?: number;
  upstream_response_headers?: string;
  upstream_response_body?: string;
  response_headers?: string;
  response_body?: string;
}

export const captureApi = {
  getSettings: () => apiClient.get<{ settings: CaptureSettings }>('/capture/settings'),
  updateSettings: (settings: CaptureSettings) =>
    apiClient.put<{ settings: CaptureSettings }>('/capture/settings', settings),
  list: (params?: { q?: string; failed_only?: boolean; limit?: number; offset?: number }) =>
    apiClient.get<{ items: CaptureRecord[] }>('/captures', { params }),
  get: (id: number) => apiClient.get<{ item: CaptureRecord }>(`/captures/${id}`),
  clear: () => apiClient.delete<{ status: string }>('/captures'),
  exportText: (params?: { q?: string; failed_only?: boolean }) =>
    apiClient.requestRaw({
      method: 'GET',
      url: '/captures/export',
      params,
      responseType: 'text',
    }),
};
