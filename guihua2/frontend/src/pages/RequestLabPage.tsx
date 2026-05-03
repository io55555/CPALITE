import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { captureApi, type CaptureRecord, type CaptureSettings } from '@/services/api';
import { useNotificationStore } from '@/stores';
import styles from './InspectorPages.module.scss';

const defaultSettings: CaptureSettings = {
  enabled: false,
  retention_days: 7,
  max_body_bytes: 65536,
};

export function RequestLabPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [settings, setSettings] = useState<CaptureSettings>(defaultSettings);
  const [items, setItems] = useState<CaptureRecord[]>([]);
  const [selected, setSelected] = useState<CaptureRecord | null>(null);
  const [query, setQuery] = useState('');
  const [failedOnly, setFailedOnly] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = async () => {
    setLoading(true);
    try {
      const [settingsResp, listResp] = await Promise.all([
        captureApi.getSettings(),
        captureApi.list({ q: query || undefined, failed_only: failedOnly, limit: 100 }),
      ]);
      setSettings(settingsResp.settings);
      setItems(listResp.items);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Request Lab load failed', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const refresh = async () => {
    await load();
  };

  const saveSettings = async (next: CaptureSettings) => {
    try {
      const resp = await captureApi.updateSettings(next);
      setSettings(resp.settings);
      showNotification('Request Lab settings updated', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to save settings', 'error');
    }
  };

  const openDetail = async (id: number) => {
    try {
      const resp = await captureApi.get(id);
      setSelected(resp.item);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to load detail', 'error');
    }
  };

  const clearAll = async () => {
    try {
      await captureApi.clear();
      setItems([]);
      setSelected(null);
      showNotification('Capture records cleared', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to clear captures', 'error');
    }
  };

  const exportAll = async () => {
    try {
      const response = await captureApi.exportText({ q: query || undefined, failed_only: failedOnly });
      const raw = typeof response.data === 'string' ? response.data : '';
      const blob = new Blob([raw], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = 'captures.txt';
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to export captures', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card title="Request Lab">
        <div className={styles.toolbar}>
          <div className={styles.toolbarGrow}>
            <Input
              label={t('common.search', { defaultValue: 'Search' })}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <ToggleSwitch
            checked={failedOnly}
            onChange={setFailedOnly}
            label="Only failed"
            ariaLabel="Only failed requests"
          />
          <Button size="sm" onClick={() => void refresh()} loading={loading}>Refresh</Button>
          <Button size="sm" variant="secondary" onClick={() => void exportAll()}>Export TXT</Button>
          <Button size="sm" variant="danger" onClick={() => void clearAll()}>Clear All</Button>
        </div>
        <div className={styles.toolbar}>
          <ToggleSwitch
            checked={settings.enabled}
            onChange={(value) => void saveSettings({ ...settings, enabled: value })}
            label="Enable capture"
            ariaLabel="Enable capture"
          />
          <div style={{ width: 140 }}>
            <Input
              label="Retention days"
              type="number"
              value={String(settings.retention_days)}
              onChange={(event) =>
                setSettings((prev) => ({ ...prev, retention_days: Number(event.target.value) || 0 }))
              }
            />
          </div>
          <div style={{ width: 160 }}>
            <Input
              label="Body bytes"
              type="number"
              value={String(settings.max_body_bytes)}
              onChange={(event) =>
                setSettings((prev) => ({ ...prev, max_body_bytes: Number(event.target.value) || 0 }))
              }
            />
          </div>
          <Button size="sm" variant="secondary" onClick={() => void saveSettings(settings)}>Save Settings</Button>
        </div>
        <p className={styles.hint}>
          Long-running mode writes captures to sqlite and truncates payload bodies by configured byte cap.
        </p>
      </Card>

      <Card title={`Captured Requests (${items.length})`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Time</th>
              <th>Status</th>
              <th>Path</th>
              <th>Provider</th>
              <th>Auth</th>
              <th>Token</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr key={item.id}>
                <td>{item.created_at}</td>
                <td className={item.success ? styles.statusGood : styles.statusBad}>
                  {item.status_code || item.upstream_status_code || 0}
                </td>
                <td>{item.method} {item.path}</td>
                <td>{item.provider || item.access_provider || '-'}</td>
                <td>{item.auth_index || item.auth_id || '-'}</td>
                <td>{item.token || item.api_key || '-'}</td>
                <td>
                  <Button size="sm" variant="secondary" onClick={() => void openDetail(item.id)}>Detail</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      <Modal
        open={selected !== null}
        title="Capture Detail"
        onClose={() => setSelected(null)}
        width={900}
      >
        {selected && (
          <div className={styles.grid}>
            <Card title="Request">
              <div className={styles.codeBlock}>{selected.request_headers || '(no request headers)'}</div>
              <div className={styles.codeBlock} style={{ marginTop: 12 }}>{selected.request_body || '(no request body)'}</div>
            </Card>
            <Card title="Upstream Request">
              <div className={styles.codeBlock}>{selected.upstream_request_url || '(no upstream url)'}</div>
              <div className={styles.codeBlock} style={{ marginTop: 12 }}>{selected.upstream_request_headers || '(no upstream request headers)'}</div>
              <div className={styles.codeBlock} style={{ marginTop: 12 }}>{selected.upstream_request_body || '(no upstream request body)'}</div>
            </Card>
            <Card title="Upstream Response">
              <div className={styles.codeBlock}>{selected.upstream_response_headers || '(no upstream response headers)'}</div>
              <div className={styles.codeBlock} style={{ marginTop: 12 }}>{selected.upstream_response_body || selected.error_text || '(no upstream response body)'}</div>
            </Card>
            <Card title="Downstream Response">
              <div className={styles.codeBlock}>{selected.response_headers || '(no downstream response headers)'}</div>
              <div className={styles.codeBlock} style={{ marginTop: 12 }}>{selected.response_body || '(no downstream response body)'}</div>
            </Card>
          </div>
        )}
      </Modal>
    </div>
  );
}
