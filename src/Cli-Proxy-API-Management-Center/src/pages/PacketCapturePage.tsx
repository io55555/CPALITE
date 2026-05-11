import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { IconTrash2 } from '@/components/ui/icons';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import {
  packetCaptureApi,
  type PacketRecord,
  type PacketRecordSummary,
  type PacketRule,
  type PacketTrigger,
} from '@/services/api/packetCapture';
import { useConfigStore } from '@/stores/useConfigStore';
import { downloadBlob } from '@/utils/download';
import styles from './PacketCapturePage.module.scss';

const ALL = '__all__';

const packetOptions = [
  { value: 'client_request', label: '客户端发给CPA' },
  { value: 'upstream_request', label: 'CPA发给供应商' },
  { value: 'upstream_response', label: '供应商返回CPA' },
  { value: 'client_response', label: 'CPA发送给客户端' },
];

const partOptions = [
  { value: 'body', label: 'Body字符串' },
  { value: 'body_json', label: 'Body JSON路径' },
  { value: 'header', label: 'HTTP头' },
  { value: 'packet', label: '完整数据包' },
];

const operatorOptions = [
  { value: 'contains', label: '包含' },
  { value: 'wildcard', label: '通配匹配' },
  { value: 'not_contains', label: '不包含' },
  { value: 'not_wildcard', label: '通配不匹配' },
  { value: 'equals', label: '完全等于' },
  { value: 'not_equals', label: '不等于' },
  { value: 'starts_with', label: '开头等于' },
  { value: 'ends_with', label: '结尾等于' },
  { value: 'num_eq', label: '数值等于' },
  { value: 'num_gt', label: '数值大于' },
  { value: 'num_gte', label: '数值大于等于' },
  { value: 'num_lt', label: '数值小于' },
  { value: 'num_lte', label: '数值小于等于' },
];

const actionOptions = [
  { value: 'record', label: '仅记录触发' },
  { value: 'block', label: '拦截请求' },
  { value: 'replace', label: '替换内容' },
  { value: 'delete', label: '删除内容' },
  { value: 'redact', label: '脱敏替换' },
  { value: 'cooldown', label: '记录冷却动作' },
  { value: 'disable', label: '禁用API Key' },
];

const targetOptions = [
  { value: 'user_token', label: '用户Token' },
  { value: 'auth', label: '账号' },
  { value: 'api_key', label: 'API Key' },
  { value: 'request', label: '请求' },
  { value: 'response', label: '响应' },
];

const replacementOptions = [
  { value: '{{original}}', label: '原始值' },
  { value: '{original}', label: '原始值(兼容)' },
  { value: '{{random_codex_ua}}', label: '随机 Codex UA' },
  { value: '{{random_claude_code_ua}}', label: '随机 Claude Code UA' },
  { value: '{{random_curl_ua}}', label: '随机 curl UA' },
  { value: '[model-redacted]', label: '模型名脱敏占位' },
  { value: '1', label: '数值 1' },
  { value: '1024', label: '数值 1024' },
];

const headerOptions = [
  'Authorization',
  'Content-Type',
  'User-Agent',
  'Accept',
  'Cache-Control',
  'X-Request-ID',
  'X-Management-Key',
];

const defaultRule: PacketRule = {
  name: '新规则',
  enabled: true,
  record_history: true,
  priority: 100,
  packet: 'client_request',
  part: 'body',
  operator: 'contains',
  value: '',
  action: 'record',
  replacement: '',
  replace_limit: 0,
  cooldown_seconds: 300,
  target: 'user_token',
};

interface RuleTemplate {
  value: string;
  label: string;
  rule: Partial<PacketRule>;
}

const ruleTemplates: RuleTemplate[] = [
  {
    value: 'block-client-header-keyword',
    label: '客户端头包含关键字就拒绝',
    rule: { name: '客户端头包含关键字就拒绝', packet: 'client_request', part: 'headers', operator: 'wildcard', value: '*关键字*', action: 'block' },
  },
  {
    value: 'block-client-header-field',
    label: '客户端某个Header包含关键字就拒绝',
    rule: { name: '客户端Header字段包含关键字就拒绝', packet: 'client_request', part: 'header', header: 'Authorization', operator: 'wildcard', value: '*关键字*', action: 'block' },
  },
  {
    value: 'block-client-body-keyword',
    label: '客户端Body包含通配关键字就拒绝',
    rule: { name: '客户端Body包含关键字就拒绝', packet: 'client_request', part: 'body', operator: 'wildcard', value: '*关键字*', action: 'block' },
  },
  {
    value: 'block-client-dialog-keyword',
    label: '客户端AI对话包含关键字就拒绝',
    rule: { name: '客户端AI对话包含关键字就拒绝', packet: 'client_request', part: 'body_json', json_path: 'messages.#.content', operator: 'wildcard', value: '*关键字*', action: 'block' },
  },
  {
    value: 'block-client-ua-keyword',
    label: '客户端UA包含通配关键字就拒绝',
    rule: { name: '客户端UA包含关键字就拒绝', packet: 'client_request', part: 'header', header: 'User-Agent', operator: 'wildcard', value: '*关键字*', action: 'block' },
  },
  {
    value: 'upstream-random-codex-ua',
    label: '供应商请求UA随机Codex版本',
    rule: { name: '供应商请求UA随机Codex版本', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_codex_ua}}' },
  },
  {
    value: 'upstream-random-claude-ua',
    label: '供应商请求UA随机Claude Code版本',
    rule: { name: '供应商请求UA随机Claude Code版本', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_claude_code_ua}}' },
  },
  {
    value: 'upstream-random-curl-ua',
    label: '供应商请求UA随机curl版本',
    rule: { name: '供应商请求UA随机curl版本', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_curl_ua}}' },
  },
  {
    value: 'upstream-model-llama-test',
    label: '替换模型为llama测试模型',
    rule: { name: '替换模型llama-3.1-8b-instant为测试模型', packet: 'upstream_request', part: 'body_json', json_path: 'model', operator: 'equals', value: 'llama-3.1-8b-instant', action: 'replace', replacement: 'llama-3.1-8b-instant-test' },
  },
  {
    value: 'upstream-append-system',
    label: '追加system提示词',
    rule: { name: '追加system提示词', packet: 'upstream_request', part: 'body_json', json_path: 'messages.0.content', operator: 'contains', value: '', action: 'replace', replacement: '{original}\n新增提示词' },
  },
  {
    value: 'upstream-append-prompt',
    label: '追加最后一条提示词',
    rule: { name: '追加提示词', packet: 'upstream_request', part: 'body', operator: 'contains', value: '"content":"', action: 'replace', replacement: '"content":"新增提示词\\n', replace_limit: 1 },
  },
  {
    value: 'upstream-top-p-1',
    label: '替换top_p为1',
    rule: { name: '替换top_p为1', packet: 'upstream_request', part: 'body_json', json_path: 'top_p', operator: 'contains', value: '', action: 'replace', replacement: '1' },
  },
  {
    value: 'upstream-max-tokens-1024',
    label: '替换max_tokens为1024',
    rule: { name: '替换max_tokens为1024', packet: 'upstream_request', part: 'body_json', json_path: 'max_tokens', operator: 'contains', value: '', action: 'replace', replacement: '1024' },
  },
  {
    value: 'client-response-delete-model-header',
    label: '客户端响应删除Header模型名',
    rule: { name: '客户端响应删除Header模型名', packet: 'client_response', part: 'header', header: 'X-Model', operator: 'contains', value: '', action: 'delete', replacement: '' },
  },
  {
    value: 'client-response-redact-model-body',
    label: '客户端响应Body隐藏模型名',
    rule: { name: '客户端响应Body隐藏模型名', packet: 'client_response', part: 'body', operator: 'contains', value: 'llama-3.1-8b-instant', action: 'redact', replacement: '[model-redacted]' },
  },
  {
    value: 'disable-key-org-restricted',
    label: '上游400 organization_restricted禁用Key',
    rule: { name: '上游400 organization_restricted禁用API Key', packet: 'upstream_response', part: 'packet', operator: 'wildcard', value: 'HTTP/* 400*organization_restricted*', action: 'disable', target: 'api_key' },
  },
  {
    value: 'disable-key-wrong-api-key',
    label: '上游401 wrong_api_key禁用Key',
    rule: { name: '上游401 wrong_api_key禁用API Key', packet: 'upstream_response', part: 'packet', operator: 'wildcard', value: 'HTTP/* 401*wrong_api_key*', action: 'disable', target: 'api_key' },
  },
  {
    value: 'disable-key-unauthorized-body',
    label: '上游401 body=unauthorized禁用Key',
    rule: { name: '上游401 unauthorized禁用API Key', packet: 'upstream_response', part: 'packet', operator: 'wildcard', value: 'HTTP/* 401*unauthorized*', action: 'disable', target: 'api_key' },
  },
];

const formatTime = (value: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
    .format(new Date(value))
    .replace(/\//g, '/')
    .replace(' ', '-');

const formatBytes = (bytes: number) => {
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
  return `${(bytes / 1024).toFixed(1)} KB`;
};

interface SuggestInputProps {
  value: string;
  options: ReadonlyArray<string | { value: string; label?: string }>;
  onChange: (value: string) => void;
  placeholder?: string;
}

function SuggestInput({ value, options, onChange, placeholder }: SuggestInputProps) {
  const id = useId();
  const normalized = useMemo(() => {
    const seen = new Set<string>();
    return options
      .map((item) => (typeof item === 'string' ? { value: item, label: item } : item))
      .filter((item) => {
        const key = item.value.trim();
        if (!key || seen.has(key)) return false;
        seen.add(key);
        return true;
      });
  }, [options]);

  return (
    <>
      <Input
        value={value}
        list={id}
        placeholder={placeholder}
        onChange={(event) => onChange(event.target.value)}
      />
      <datalist id={id}>
        {normalized.map((item) => (
          <option key={item.value} value={item.value} label={item.label} />
        ))}
      </datalist>
    </>
  );
}

export function PacketCapturePage() {
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const config = useConfigStore((state) => state.config);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [records, setRecords] = useState<PacketRecordSummary[]>([]);
  const [rules, setRules] = useState<PacketRule[]>([]);
  const [triggers, setTriggers] = useState<PacketTrigger[]>([]);
  const [selectedIds, setSelectedIds] = useState<Record<string, true>>({});
  const [selectedTriggerIds, setSelectedTriggerIds] = useState<Record<string, true>>({});
  const [modelFilter, setModelFilter] = useState(ALL);
  const [sourceFilter, setSourceFilter] = useState(ALL);
  const [resultFilter, setResultFilter] = useState(ALL);
  const [autoRefresh, setAutoRefresh] = useState('off');
  const [detail, setDetail] = useState<PacketRecord | null>(null);
  const [triggerDetail, setTriggerDetail] = useState<PacketRecord | null>(null);
  const [triggerDetailError, setTriggerDetailError] = useState('');
  const [editingRule, setEditingRule] = useState<PacketRule | null>(null);
  const [triggerPageSize, setTriggerPageSize] = useState('50');
  const [triggerPage, setTriggerPage] = useState(1);

  useEffect(() => {
    void fetchConfig(undefined, false).catch(() => undefined);
  }, [fetchConfig]);

  const load = useCallback(async () => {
    const [state, recordList, ruleList, triggerList] = await Promise.all([
      packetCaptureApi.getState(),
      packetCaptureApi.listRecords({
        model: modelFilter,
        source: sourceFilter,
        result: resultFilter,
        limit: 500,
      }),
      packetCaptureApi.listRules(),
      packetCaptureApi.listTriggers({ limit: 5000 }),
    ]);
    setEnabled(Boolean(state.enabled));
    setRecords(Array.isArray(recordList) ? recordList : []);
    setRules(Array.isArray(ruleList) ? ruleList : []);
    setTriggers(Array.isArray(triggerList) ? triggerList : []);
  }, [modelFilter, resultFilter, sourceFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const ms = autoRefresh === '15s' ? 15000 : autoRefresh === '30s' ? 30000 : autoRefresh === '1m' ? 60000 : 0;
    if (!ms) return;
    const timer = window.setInterval(() => void load(), ms);
    return () => window.clearInterval(timer);
  }, [autoRefresh, load]);

  const modelOptions = useMemo(
    () => [
      { value: ALL, label: '全部模型' },
      ...Array.from(new Set(records.map((item) => item.model).filter(Boolean))).map((value) => ({
        value,
        label: value,
      })),
    ],
    [records]
  );
  const providerSuggestions = useMemo(() => {
    const values = new Set<string>();
    config?.openaiCompatibility?.forEach((provider) => {
      if (provider.name) values.add(provider.name);
    });
    records.forEach((item) => {
      if (item.provider) values.add(item.provider);
      if (item.source) values.add(item.source);
    });
    rules.forEach((rule) => {
      if (rule.provider) values.add(rule.provider);
    });
    return Array.from(values).sort((a, b) => a.localeCompare(b));
  }, [config?.openaiCompatibility, records, rules]);
  const modelSuggestions = useMemo(() => {
    const values = new Set<string>();
    config?.openaiCompatibility?.forEach((provider) => {
      provider.models?.forEach((model) => {
        if (model.name) values.add(model.name);
        if (model.alias) values.add(model.alias);
      });
    });
    records.forEach((item) => {
      if (item.model) values.add(item.model);
    });
    rules.forEach((rule) => {
      if (rule.model) values.add(rule.model);
      if (rule.model_keyword) values.add(rule.model_keyword);
    });
    return Array.from(values).sort((a, b) => a.localeCompare(b));
  }, [config?.openaiCompatibility, records, rules]);
  const sourceOptions = useMemo(
    () => [
      { value: ALL, label: '全部来源' },
      ...Array.from(new Set(records.map((item) => item.source || item.provider).filter(Boolean))).map((value) => ({
        value,
        label: value,
      })),
    ],
    [records]
  );
  const selectedList = Object.keys(selectedIds);
  const allSelected = records.length > 0 && records.every((row) => selectedIds[row.id]);
  const selectedTriggerList = Object.keys(selectedTriggerIds);
  const triggerSize = Math.max(1, Number(triggerPageSize) || 50);
  const triggerPageCount = Math.max(1, Math.ceil(triggers.length / triggerSize));
  const normalizedTriggerPage = Math.min(triggerPage, triggerPageCount);
  const triggerPageItems = triggers.slice((normalizedTriggerPage - 1) * triggerSize, normalizedTriggerPage * triggerSize);

  useEffect(() => {
    setTriggerPage(1);
  }, [triggerPageSize]);

  useEffect(() => {
    if (triggerPage > triggerPageCount) {
      setTriggerPage(triggerPageCount);
    }
  }, [triggerPage, triggerPageCount]);

  const deleteIds = async (ids: string[]) => {
    if (ids.length === 0) return;
    await packetCaptureApi.deleteRecords(ids);
    setSelectedIds({});
    await load();
  };

  const saveRule = async () => {
    if (!editingRule) return;
    await packetCaptureApi.saveRule(editingRule);
    setEditingRule(null);
    await load();
  };

  const exportRules = async () => {
    const response = await packetCaptureApi.exportRules();
    const blob = response.data instanceof Blob ? response.data : new Blob([response.data]);
    downloadBlob({
      filename: `packet-filter-rules-${new Date().toISOString().slice(0, 10)}.json`,
      blob,
    });
  };

  const importRules = async (file: File | null) => {
    if (!file) return;
    await packetCaptureApi.importRules(file);
    await load();
  };

  const deleteTriggerIds = async (ids: string[]) => {
    if (ids.length === 0) return;
    await packetCaptureApi.deleteTriggers(ids);
    setSelectedTriggerIds({});
    await load();
  };

  const toggleTriggerPageSelection = () => {
    if (triggerPageItems.length === 0) return;
    setSelectedTriggerIds((prev) => {
      const next = { ...prev };
      const allPageSelected = triggerPageItems.every((item) => next[item.id]);
      triggerPageItems.forEach((item) => {
        if (allPageSelected) {
          delete next[item.id];
        } else {
          next[item.id] = true;
        }
      });
      return next;
    });
  };

  const toggleRuleEnabled = async (rule: PacketRule, enabled: boolean) => {
    setRules((prev) => prev.map((item) => (item.id === rule.id ? { ...item, enabled } : item)));
    await packetCaptureApi.saveRule({ ...rule, enabled });
    await load();
  };

  const showTriggerDetail = async (item: PacketTrigger) => {
    setTriggerDetailError('');
    setTriggerDetail(null);
    try {
      try {
        setTriggerDetail(await packetCaptureApi.getRecord(item.record_id));
        return;
      } catch {
        const matches = await packetCaptureApi.listRecords({ request_id: item.record_id, limit: 1 });
        if (matches.length > 0) {
          setTriggerDetail(await packetCaptureApi.getRecord(matches[0].id));
          return;
        }
      }
      setTriggerDetailError('未找到对应抓包记录。触发历史可能早于抓包记录写入，或相关抓包记录已被删除。');
    } catch {
      setTriggerDetailError('读取触发详情失败');
    }
  };

  const applyRuleTemplate = (value: string) => {
    const template = ruleTemplates.find((item) => item.value === value);
    if (!template || !editingRule) return;
    setEditingRule({
      ...editingRule,
      ...template.rule,
      enabled: editingRule.enabled,
      record_history: editingRule.record_history ?? true,
      priority: editingRule.priority,
    });
  };

  return (
    <div className={styles.container}>
      <header className={styles.header}>
        <h1>抓包/过滤</h1>
        <Button variant="secondary" onClick={() => void load()}>刷新</Button>
      </header>

      <Card
        title={
          <div className={styles.captureTitle}>
            <span>抓包</span>
            <label className={styles.switch}>
              <input
                type="checkbox"
                checked={enabled}
                onChange={async (event) => {
                  const next = event.target.checked;
                  await packetCaptureApi.setState(next);
                  setEnabled(next);
                }}
              />
              <span className={styles.switchTrack} aria-hidden="true" />
              <span className={styles.switchText}>开启抓包</span>
            </label>
          </div>
        }
        extra={
          <div className={styles.actions}>
            <Button variant="secondary" size="sm" onClick={() => void deleteIds(selectedList)} disabled={selectedList.length === 0}>删除勾选条目</Button>
            <Button variant="secondary" size="sm" onClick={() => void deleteIds(records.map((row) => row.id))} disabled={records.length === 0}>删除当前页条目</Button>
            <Button variant="secondary" size="sm" onClick={async () => { await packetCaptureApi.deleteAllRecords(); await load(); }} disabled={records.length === 0}>删除所有条目</Button>
          </div>
        }
      >
        <div className={styles.filters}>
          <Select value={modelFilter} options={modelOptions} onChange={setModelFilter} ariaLabel="模型筛选" />
          <Select value={sourceFilter} options={sourceOptions} onChange={setSourceFilter} ariaLabel="来源筛选" />
          <Select
            value={resultFilter}
            options={[
              { value: ALL, label: '全部结果' },
              { value: 'success', label: '成功' },
              { value: 'failed', label: '失败' },
            ]}
            onChange={setResultFilter}
            ariaLabel="结果筛选"
          />
          <Select
            value={autoRefresh}
            options={[
              { value: 'off', label: '关闭自动刷新' },
              { value: '15s', label: '15秒' },
              { value: '30s', label: '30秒' },
              { value: '1m', label: '1分钟' },
            ]}
            onChange={setAutoRefresh}
            ariaLabel="自动刷新"
          />
        </div>

        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th><input type="checkbox" checked={allSelected} onChange={() => setSelectedIds(allSelected ? {} : Object.fromEntries(records.map((r) => [r.id, true])) as Record<string, true>)} /></th>
                <th>时间</th>
                <th>大小</th>
                <th>响应码</th>
                <th>模型</th>
                <th>用户Token</th>
                <th>账号/渠道/APIKey</th>
                <th>客户端UA</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {records.map((row) => (
                <tr key={row.id}>
                  <td><input type="checkbox" checked={Boolean(selectedIds[row.id])} onChange={() => setSelectedIds((prev) => {
                    const next = { ...prev };
                    if (next[row.id]) delete next[row.id]; else next[row.id] = true;
                    return next;
                  })} /></td>
                  <td>{formatTime(row.timestamp)}</td>
                  <td>{formatBytes(row.total_bytes)}</td>
                  <td className={row.failed ? styles.failed : styles.success}>{row.upstream_status_code || '-'}</td>
                  <td>{row.model || '-'}</td>
                  <td title={row.user_token}>{row.user_token || '-'}</td>
                  <td title={`${row.auth_label || ''} ${row.provider || ''} ${row.api_key || ''}`}>{row.auth_label || row.provider || row.api_key || '-'}</td>
                  <td title={row.client_ua}>{row.client_ua || '-'}</td>
                  <td>
                    <div className={styles.rowActions}>
                      <Button size="sm" variant="secondary" onClick={async () => setDetail(await packetCaptureApi.getRecord(row.id))}>查看数据包</Button>
                      <button
                        type="button"
                        className={styles.iconButton}
                        title="删除"
                        onClick={() => void deleteIds([row.id])}
                      >
                        <IconTrash2 size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Card
        title="过滤规则"
        extra={
          <div className={styles.actions}>
            <input
              ref={importInputRef}
              type="file"
              accept="application/json,.json"
              className={styles.hiddenFileInput}
              onChange={(event) => {
                const file = event.target.files?.[0] || null;
                void importRules(file).finally(() => {
                  event.target.value = '';
                });
              }}
            />
            <Button size="sm" variant="secondary" onClick={() => void exportRules()} disabled={rules.length === 0}>导出规则</Button>
            <Button size="sm" variant="secondary" onClick={() => importInputRef.current?.click()}>导入规则</Button>
            <Button size="sm" onClick={() => setEditingRule(defaultRule)}>添加规则</Button>
          </div>
        }
      >
        <div className={styles.ruleGrid}>
          {rules.map((rule) => (
            <div className={styles.ruleItem} key={rule.id}>
              <div>
                <div className={styles.ruleName}>{rule.name}</div>
                <span>{rule.record_history ?? true ? '记录触发历史' : '不记录触发历史'} · {rule.packet} · {rule.part} · {rule.operator} · {rule.action}</span>
              </div>
              <div className={styles.actions}>
                <label className={styles.switch} title={rule.enabled ? '停用规则' : '启用规则'}>
                  <input
                    type="checkbox"
                    checked={rule.enabled}
                    onChange={(event) => void toggleRuleEnabled(rule, event.target.checked)}
                  />
                  <span className={styles.switchTrack} aria-hidden="true" />
                  <span className={styles.switchText}>{rule.enabled ? '启用' : '停用'}</span>
                </label>
                <Button size="sm" variant="secondary" onClick={() => setEditingRule(rule)}>编辑</Button>
                <Button size="sm" variant="secondary" onClick={async () => { if (rule.id) await packetCaptureApi.deleteRule(rule.id); await load(); }}>删除</Button>
              </div>
            </div>
          ))}
        </div>

        <div className={styles.triggerHeader}>
          <h2 className={styles.subTitle}>规则触发历史</h2>
          <div className={styles.actions}>
            <Select
              value={triggerPageSize}
              options={[
                { value: '20', label: '20条/页' },
                { value: '50', label: '50条/页' },
                { value: '100', label: '100条/页' },
                { value: '200', label: '200条/页' },
              ]}
              onChange={setTriggerPageSize}
              ariaLabel="每页条数"
            />
            <Button variant="secondary" size="sm" onClick={toggleTriggerPageSelection} disabled={triggerPageItems.length === 0}>全选反选</Button>
            <Button variant="secondary" size="sm" onClick={() => void deleteTriggerIds(selectedTriggerList)} disabled={selectedTriggerList.length === 0}>删除勾选</Button>
            <Button variant="secondary" size="sm" onClick={() => void deleteTriggerIds(triggerPageItems.map((item) => item.id))} disabled={triggerPageItems.length === 0}>删除本页</Button>
            <Button variant="secondary" size="sm" onClick={async () => { await packetCaptureApi.deleteAllTriggers(); setSelectedTriggerIds({}); await load(); }} disabled={triggers.length === 0}>删除所有</Button>
          </div>
        </div>
        <div className={styles.triggerList}>
          {triggerPageItems.map((item) => (
            <div className={styles.triggerItem} key={item.id}>
              <input
                type="checkbox"
                checked={Boolean(selectedTriggerIds[item.id])}
                onChange={() => setSelectedTriggerIds((prev) => {
                  const next = { ...prev };
                  if (next[item.id]) delete next[item.id]; else next[item.id] = true;
                  return next;
                })}
              />
              <span>{formatTime(item.timestamp)}</span>
              <strong>{item.rule_name}</strong>
              <span>{item.action}</span>
              <span>{item.target || '-'}</span>
              <span>{item.detail}</span>
              <Button size="sm" variant="secondary" onClick={() => void showTriggerDetail(item)}>详情</Button>
              <button
                type="button"
                className={styles.iconButton}
                title="删除"
                onClick={() => void deleteTriggerIds([item.id])}
              >
                <IconTrash2 size={14} />
              </button>
            </div>
          ))}
        </div>
        {triggers.length > triggerSize && (
          <div className={styles.pagination}>
            <Button variant="secondary" size="sm" onClick={() => setTriggerPage((page) => Math.max(1, page - 1))} disabled={normalizedTriggerPage <= 1}>上一页</Button>
            <span>第 {normalizedTriggerPage} / {triggerPageCount} 页，共 {triggers.length} 条</span>
            <Button variant="secondary" size="sm" onClick={() => setTriggerPage((page) => Math.min(triggerPageCount, page + 1))} disabled={normalizedTriggerPage >= triggerPageCount}>下一页</Button>
          </div>
        )}
      </Card>

      <Modal open={detail !== null} title="查看数据包" onClose={() => setDetail(null)} width={760}>
        {detail && packetOptions.map((item) => (
          <div className={styles.packetBlock} key={item.value}>
            <h3>{item.label}</h3>
            <pre>{detail.packets[item.value as keyof typeof detail.packets] || '-'}</pre>
          </div>
        ))}
      </Modal>

      <Modal open={triggerDetail !== null || triggerDetailError !== ''} title="规则触发详情" onClose={() => { setTriggerDetail(null); setTriggerDetailError(''); }} width={760}>
        {triggerDetailError && <p className={styles.errorText}>{triggerDetailError}</p>}
        {triggerDetail && packetOptions.map((item) => (
          <div className={styles.packetBlock} key={item.value}>
            <h3>{item.label}</h3>
            <pre>{triggerDetail.packets[item.value as keyof typeof triggerDetail.packets] || '-'}</pre>
          </div>
        ))}
      </Modal>

      <Modal open={editingRule !== null} title="过滤规则" onClose={() => setEditingRule(null)} width={720}>
        {editingRule && (
          <div className={styles.ruleForm}>
            <label className={styles.full}>常见模板<Select value="" options={[{ value: '', label: '选择模板快速套用' }, ...ruleTemplates.map((item) => ({ value: item.value, label: item.label }))]} onChange={applyRuleTemplate} ariaLabel="常见模板" /></label>
            <label>规则名称<Input value={editingRule.name} onChange={(event) => setEditingRule({ ...editingRule, name: event.target.value })} /></label>
            <label>启用<input type="checkbox" checked={editingRule.enabled} onChange={(event) => setEditingRule({ ...editingRule, enabled: event.target.checked })} /></label>
            <label>记录触发历史<input type="checkbox" checked={editingRule.record_history ?? true} onChange={(event) => setEditingRule({ ...editingRule, record_history: event.target.checked })} /></label>
            <label>优先级<Input value={String(editingRule.priority)} onChange={(event) => setEditingRule({ ...editingRule, priority: Number(event.target.value) || 100 })} /></label>
            <label>指定渠道<SuggestInput value={editingRule.provider || ''} options={providerSuggestions} onChange={(value) => setEditingRule({ ...editingRule, provider: value })} /></label>
            <label>渠道包含<Input value={editingRule.provider_keyword || ''} onChange={(event) => setEditingRule({ ...editingRule, provider_keyword: event.target.value })} /></label>
            <label>指定模型<SuggestInput value={editingRule.model || ''} options={modelSuggestions} onChange={(value) => setEditingRule({ ...editingRule, model: value })} /></label>
            <label>模型包含<SuggestInput value={editingRule.model_keyword || ''} options={modelSuggestions} onChange={(value) => setEditingRule({ ...editingRule, model_keyword: value })} /></label>
            <label>检查数据包<Select value={editingRule.packet} options={packetOptions} onChange={(value) => setEditingRule({ ...editingRule, packet: value })} ariaLabel="检查数据包" /></label>
            <label>检查位置<Select value={editingRule.part} options={partOptions} onChange={(value) => setEditingRule({ ...editingRule, part: value })} ariaLabel="检查位置" /></label>
            <label>Header名<SuggestInput value={editingRule.header || ''} options={headerOptions} onChange={(value) => setEditingRule({ ...editingRule, header: value })} /></label>
            <label>JSON路径<Input value={editingRule.json_path || ''} onChange={(event) => setEditingRule({ ...editingRule, json_path: event.target.value })} placeholder="message.3.error.res" /></label>
            <label>判断<Select value={editingRule.operator} options={operatorOptions} onChange={(value) => setEditingRule({ ...editingRule, operator: value })} ariaLabel="判断" /></label>
            <label>匹配值<Input value={editingRule.value || ''} onChange={(event) => setEditingRule({ ...editingRule, value: event.target.value })} /></label>
            <label>数值<Input value={String(editingRule.value_number || 0)} onChange={(event) => setEditingRule({ ...editingRule, value_number: Number(event.target.value) || 0 })} /></label>
            <label>动作<Select value={editingRule.action} options={actionOptions} onChange={(value) => setEditingRule({ ...editingRule, action: value })} ariaLabel="动作" /></label>
            <label>替换为<SuggestInput value={editingRule.replacement || ''} options={replacementOptions} onChange={(value) => setEditingRule({ ...editingRule, replacement: value })} placeholder="{{random_curl_ua}}" /></label>
            <label>替换次数<Input value={String(editingRule.replace_limit || 0)} onChange={(event) => setEditingRule({ ...editingRule, replace_limit: Number(event.target.value) || 0 })} /></label>
            <label>冷却秒数<Input value={String(editingRule.cooldown_seconds || 0)} onChange={(event) => setEditingRule({ ...editingRule, cooldown_seconds: Number(event.target.value) || 0 })} /></label>
            <label>操作目标<SuggestInput value={editingRule.target || ''} options={targetOptions} onChange={(value) => setEditingRule({ ...editingRule, target: value })} placeholder="user_token / auth / api_key" /></label>
            <label className={styles.full}>备注<Input value={editingRule.notes || ''} onChange={(event) => setEditingRule({ ...editingRule, notes: event.target.value })} /></label>
            <div className={styles.formActions}>
              <Button variant="secondary" onClick={() => setEditingRule(null)}>取消</Button>
              <Button onClick={() => void saveRule()}>保存</Button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}
