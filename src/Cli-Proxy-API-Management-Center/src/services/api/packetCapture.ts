import { apiClient } from './client';

export interface PacketSet {
  client_request: string;
  upstream_request: string;
  upstream_response: string;
  client_response: string;
}

export interface PacketCaptureState {
  enabled: boolean;
  'cli-detailed-log': boolean;
}

export interface PacketRecordSummary {
  id: string;
  timestamp: string;
  request_id?: string;
  provider: string;
  source?: string;
  model: string;
  user_token?: string;
  auth_label?: string;
  auth_type?: string;
  auth_index?: string;
  api_key?: string;
  client_ua?: string;
  endpoint?: string;
  upstream_status_code: number;
  failed: boolean;
  total_bytes: number;
  summary?: string;
}

export interface PacketRecord extends PacketRecordSummary {
  packets: PacketSet;
}

export interface PacketRule {
  id?: string;
  name: string;
  enabled: boolean;
  record_history?: boolean;
  priority: number;
  provider?: string;
  provider_keyword?: string;
  model?: string;
  model_keyword?: string;
  packet: string;
  part: string;
  json_path?: string;
  header?: string;
  operator: string;
  value?: string;
  value_number?: number;
  action: string;
  replacement?: string;
  replace_limit?: number;
  cooldown_seconds?: number;
  target?: string;
  notes?: string;
  created_at?: string;
  updated_at?: string;
}

export interface PacketTrigger {
  id: string;
  rule_id: string;
  rule_name: string;
  record_id: string;
  timestamp: string;
  action: string;
  target?: string;
  detail?: string;
}

export const packetCaptureApi = {
  getState: () => apiClient.get<PacketCaptureState>('/packet-capture/state'),
  setState: (payload: { enabled?: boolean; 'cli-detailed-log'?: boolean }) =>
    apiClient.put<PacketCaptureState>('/packet-capture/state', payload),
  listRecords: (params?: Record<string, string | number>) =>
    apiClient.get<PacketRecordSummary[]>('/packet-capture/records', { params }),
  getRecord: (id: string) => apiClient.get<PacketRecord>(`/packet-capture/records/${id}`),
  deleteRecords: (ids: string[]) => apiClient.delete('/packet-capture/records', { data: { ids } }),
  deleteAllRecords: () => apiClient.delete('/packet-capture/records', { data: { all: true } }),
  listRules: () => apiClient.get<PacketRule[]>('/packet-capture/rules'),
  exportRules: () =>
    apiClient.getRaw('/packet-capture/rules/export', { responseType: 'blob' }),
  importRules: (file: File) => {
    const formData = new FormData();
    formData.append('file', file);
    return apiClient.postForm<{ imported: number }>('/packet-capture/rules/import', formData);
  },
  saveRule: (rule: PacketRule) => apiClient.put<PacketRule>('/packet-capture/rules', rule),
  deleteRule: (id: string) => apiClient.delete(`/packet-capture/rules/${id}`),
  listTriggers: (params?: { limit?: number }) =>
    apiClient.get<PacketTrigger[]>('/packet-capture/triggers', { params: { limit: 5000, ...params } }),
  deleteTriggers: (ids: string[]) => apiClient.delete('/packet-capture/triggers', { data: { ids } }),
  deleteAllTriggers: () => apiClient.delete('/packet-capture/triggers', { data: { all: true } }),
};
