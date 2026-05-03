import { apiClient } from './client';

export interface StatusRule {
  id?: number;
  name: string;
  enabled: boolean;
  provider?: string;
  auth_index?: string;
  status_code?: number;
  body_contains?: string;
  action: string;
  cooldown_seconds?: number;
  created_at?: string;
  updated_at?: string;
}

export interface StatusRuleHit {
  id: number;
  created_at: string;
  rule_id: number;
  rule_name: string;
  action: string;
  provider?: string;
  auth_id?: string;
  auth_index?: string;
  status_code?: number;
  message?: string;
}

export const statusRulerApi = {
  listRules: () => apiClient.get<{ items: StatusRule[] }>('/status-ruler/rules'),
  saveRule: (rule: StatusRule) => apiClient.put<{ item: StatusRule }>('/status-ruler/rules', rule),
  deleteRule: (id: number) => apiClient.delete<{ status: string }>(`/status-ruler/rules/${id}`),
  listHits: (limit = 100) => apiClient.get<{ items: StatusRuleHit[] }>('/status-ruler/hits', { params: { limit } }),
};
