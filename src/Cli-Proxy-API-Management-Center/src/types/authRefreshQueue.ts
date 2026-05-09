export interface AuthRefreshQueueItem {
  id: string;
  auth_index: string;
  name: string;
  provider: string;
  status: string;
  unavailable: boolean;
  disabled: boolean;
  next_refresh_at: string;
  account_type?: string;
  account?: string;
  email?: string;
}

export interface AuthRefreshQueueResponse {
  queue: AuthRefreshQueueItem[];
  count: number;
  generated_at: string;
}
