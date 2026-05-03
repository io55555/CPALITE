import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { breakerApi, type BreakerState } from '@/services/api';
import { useNotificationStore } from '@/stores';
import styles from './InspectorPages.module.scss';

export function BreakerPage() {
  const { showNotification } = useNotificationStore();
  const [items, setItems] = useState<BreakerState[]>([]);
  const [loading, setLoading] = useState(true);

  const load = async () => {
    setLoading(true);
    try {
      const resp = await breakerApi.list();
      setItems(resp.items);
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '熔断状态加载失败', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const reset = async (scope?: string, key?: string) => {
    try {
      await breakerApi.reset(scope, key);
      await load();
      showNotification('熔断状态已重置', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : '重置熔断状态失败', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card
        title="IP / 代理熔断"
        extra={
          <div className={styles.actions}>
            <Button size="sm" onClick={() => void load()} loading={loading}>刷新</Button>
            <Button size="sm" variant="secondary" onClick={() => void reset()}>全部重置</Button>
          </div>
        }
      >
        <p className={styles.hint}>
          当账号级代理 IP 或认证连续故障时，熔断器会进入 open / half-open 状态，避免并发请求持续打到同一个故障账号。
        </p>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>范围</th>
              <th>键</th>
              <th>状态</th>
              <th>失败次数</th>
              <th>冷却截止</th>
              <th>最后错误</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item) => (
              <tr key={`${item.scope}:${item.key}`}>
                <td>{item.scope}</td>
                <td>{item.key}</td>
                <td className={item.status === 'closed' ? styles.statusGood : styles.statusBad}>
                  {item.status}{item.probe_in_flight ? ' / probe' : ''}
                </td>
                <td>{item.failure_count}</td>
                <td>{item.cooldown_until || '-'}</td>
                <td>{item.last_error || '-'}</td>
                <td>
                  <Button size="sm" variant="secondary" onClick={() => void reset(item.scope, item.key)}>重置</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
