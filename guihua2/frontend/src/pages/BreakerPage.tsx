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
      showNotification(error instanceof Error ? error.message : 'Breaker load failed', 'error');
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
      showNotification('Breaker state reset', 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : 'Failed to reset breaker', 'error');
    }
  };

  return (
    <div className={styles.page}>
      <Card
        title="Breaker"
        extra={
          <div className={styles.actions}>
            <Button size="sm" onClick={() => void load()} loading={loading}>Refresh</Button>
            <Button size="sm" variant="secondary" onClick={() => void reset()}>Reset All</Button>
          </div>
        }
      >
        <p className={styles.hint}>
          Proxy/auth breaker uses open and half-open states to avoid concurrent retries repeatedly hitting a broken account proxy.
        </p>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Scope</th>
              <th>Key</th>
              <th>Status</th>
              <th>Failures</th>
              <th>Cooldown</th>
              <th>Last Error</th>
              <th>Action</th>
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
                  <Button size="sm" variant="secondary" onClick={() => void reset(item.scope, item.key)}>Reset</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
    </div>
  );
}
