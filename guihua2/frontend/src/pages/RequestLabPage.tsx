import { useEffect, useState } from 'react';
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

const formatStatus = (item: CaptureRecord) => item.status_code || item.upstream_status_code || 0;

const renderPacketBlock = (title: string, summary: Array<string | number>, body: string) => (
  <Card title={title}>
    <div className={styles.codeBlock}>{summary.filter(Boolean).join('\n') || '(empty)'}</div>
    <div className={styles.codeBlock} style={{ marginTop: 12 }}>
      {body || '(empty)'}
    </div>
  </Card>
);

export function RequestLabPage() {
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
      showNotification(error instanceof Error ? error.message : '抓包记录加载失败', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const saveSettings = async (next: CaptureSettings) => {
    try {
      const resp = await captureApi.updateSettings(next);
      setSettings(resp.settings);
      showNotification('抓包设置已更新', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '保存抓包设置失败', 'error');
    }
  };

  const openDetail = async (id: number) => {
    try {
      const resp = await captureApi.get(id);
      setSelected(resp.item);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '加载抓包详情失败', 'error');
    }
  };

  const clearAll = async () => {
    try {
      await captureApi.clear();
      setItems([]);
      setSelected(null);
      showNotification('抓包记录已清空', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '清空抓包记录失败', 'error');
    }
  };

  const exportAll = async () => {
    try {
      const response = await captureApi.exportText({
        q: query || undefined,
        failed_only: failedOnly,
      });
      const raw = typeof response.data === 'string' ? response.data : '';
      const blob = new Blob([raw], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = 'captures.txt';
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '导出抓包记录失败', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card title="抓包 / 过滤">
        <div className={styles.toolbar}>
          <div className={styles.toolbarGrow}>
            <Input
              label="筛选关键字"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <ToggleSwitch
            checked={failedOnly}
            onChange={setFailedOnly}
            label="仅错误请求"
            ariaLabel="仅错误请求"
          />
          <Button size="sm" onClick={() => void load()} loading={loading}>
            刷新
          </Button>
          <Button size="sm" variant="secondary" onClick={() => void exportAll()}>
            导出 TXT
          </Button>
          <Button size="sm" variant="danger" onClick={() => void clearAll()}>
            清空全部
          </Button>
        </div>
        <div className={styles.toolbar}>
          <ToggleSwitch
            checked={settings.enabled}
            onChange={(value) => void saveSettings({ ...settings, enabled: value })}
            label="启用抓包"
            ariaLabel="启用抓包"
          />
          <div style={{ width: 140 }}>
            <Input
              label="保留天数"
              type="number"
              value={String(settings.retention_days)}
              onChange={(event) =>
                setSettings((prev) => ({
                  ...prev,
                  retention_days: Number(event.target.value) || 0,
                }))
              }
            />
          </div>
          <div style={{ width: 160 }}>
            <Input
              label="包体字节上限"
              type="number"
              value={String(settings.max_body_bytes)}
              onChange={(event) =>
                setSettings((prev) => ({
                  ...prev,
                  max_body_bytes: Number(event.target.value) || 0,
                }))
              }
            />
          </div>
          <Button size="sm" variant="secondary" onClick={() => void saveSettings(settings)}>
            保存设置
          </Button>
        </div>
        <p className={styles.hint}>
          抓包数据会持久化到 sqlite，并按保留天数和包体大小自动截断，避免长期运行时内存和磁盘无界增长。
        </p>
      </Card>

      <Card title={`抓包记录（${items.length}）`}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>时间</th>
              <th>状态</th>
              <th>下游请求</th>
              <th>上游目标</th>
              <th>供应商</th>
              <th>认证</th>
              <th>Token / Key</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr key={item.id}>
                <td>{item.created_at}</td>
                <td className={item.success ? styles.statusGood : styles.statusBad}>
                  {formatStatus(item)}
                </td>
                <td>
                  {item.method} {item.path}
                </td>
                <td>{item.upstream_request_url || '-'}</td>
                <td>{item.provider || item.access_provider || '-'}</td>
                <td>{item.auth_index || item.auth_id || '-'}</td>
                <td>{item.token || item.api_key || '-'}</td>
                <td>
                  <Button size="sm" variant="secondary" onClick={() => void openDetail(item.id)}>
                    详情
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>

      <Modal open={selected !== null} title="抓包详情" onClose={() => setSelected(null)} width={1100}>
        {selected && (
          <div className={styles.grid}>
            {renderPacketBlock(
              '下游请求',
              [
                `时间: ${selected.created_at}`,
                `请求: ${selected.method} ${selected.path}`,
                `状态码: ${selected.status_code || 0}`,
              ],
              [selected.request_headers, '', selected.request_body].filter(Boolean).join('\n')
            )}
            {renderPacketBlock(
              '上游请求',
              [
                `目标: ${selected.upstream_request_url || '-'}`,
                `认证: ${selected.auth_index || selected.auth_id || '-'}`,
                `代理: ${selected.proxy_url || '-'}`,
              ],
              [selected.upstream_request_headers, '', selected.upstream_request_body]
                .filter(Boolean)
                .join('\n')
            )}
            {renderPacketBlock(
              '上游响应',
              [
                `上游状态码: ${selected.upstream_status_code || 0}`,
                `成功: ${selected.success ? '是' : '否'}`,
                selected.error_text ? `错误: ${selected.error_text}` : '',
              ],
              [selected.upstream_response_headers, '', selected.upstream_response_body || selected.error_text]
                .filter(Boolean)
                .join('\n')
            )}
            {renderPacketBlock(
              '下游响应',
              [
                `下游状态码: ${selected.status_code || 0}`,
                `耗时: ${selected.duration_ms} ms`,
              ],
              [selected.response_headers, '', selected.response_body].filter(Boolean).join('\n')
            )}
          </div>
        )}
      </Modal>
    </div>
  );
}
