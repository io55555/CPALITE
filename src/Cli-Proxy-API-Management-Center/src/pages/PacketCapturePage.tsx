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
  type PacketRuleAction,
  type PacketRuleCondition,
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
  { value: 'status', label: '响应码' },
  { value: 'path', label: '请求路径' },
  { value: 'body', label: 'Body字符串' },
  { value: 'body_json', label: 'Body JSON路径' },
  { value: 'header', label: 'HTTP头' },
  { value: 'headers', label: '所有HTTP头' },
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
  { value: 'block', label: '拦截请求(向客户端返回403)' },
  { value: 'replace', label: '替换内容' },
  { value: 'delete', label: '删除内容' },
  { value: 'redact', label: '脱敏替换' },
  { value: 'cooldown', label: '冷却目标(*s/*h/*d见秒数)' },
  { value: 'disable', label: '禁用目标' },
  { value: 'return_clean_400', label: '返回客户端纯净400' },
  { value: 'return_clean_401', label: '返回客户端纯净401' },
  { value: 'return_clean_429', label: '返回客户端纯净429' },
  { value: 'return_clean_500', label: '返回客户端纯净500' },
];

const targetOptions = [
  { value: 'user_token', label: '用户Token' },
  { value: 'auth', label: '认证文件/账号' },
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
  { value: 'Upstream provider error', label: '上游错误脱敏占位' },
  { value: 'Internal Server Error', label: '纯净500消息' },
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
  match_logic: 'all',
  conditions: [
    { packet: 'client_request', part: 'body', operator: 'contains', value: '' },
  ],
  actions: [
    { type: 'record', packet: 'client_request', part: 'body' },
  ],
};

interface RuleTemplate {
  value: string;
  label: string;
  rule: Partial<PacketRule>;
}

const ruleTemplates: RuleTemplate[] = [
  {
    value: 'upstream-status-200-record',
    label: '上游响应码200仅记录',
    rule: { name: '[运营商到CPA]响应码200仅记录', packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 200, action: 'record', notes: '使用已解析响应码精确匹配，避免扫描完整HTTP包。' },
  },
  {
    value: 'upstream-status-400-record',
    label: '上游响应码400仅记录',
    rule: { name: '[运营商到CPA]响应码400仅记录', packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 400, action: 'record' },
  },
  {
    value: 'upstream-status-401-disable',
    label: '上游响应码401禁用API Key',
    rule: { name: '[运营商到CPA]响应码401禁用API Key', packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 401, action: 'disable', target: 'api_key' },
  },
  {
    value: 'upstream-status-429-cooldown',
    label: '上游响应码429冷却API Key',
    rule: { name: '[运营商到CPA]响应码429冷却API Key 300s', packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 429, action: 'cooldown', target: 'api_key', cooldown_seconds: 300 },
  },
  {
    value: 'upstream-status-and-body-disable',
    label: '上游响应码+Body组合禁用Key',
    rule: {
      name: '[运营商到CPA]401且Body含invalid禁用API Key',
      packet: 'upstream_response',
      part: 'status',
      operator: 'num_eq',
      value_number: 401,
      action: 'disable',
      target: 'api_key',
      match_logic: 'all',
      conditions: [
        { packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 401 },
        { packet: 'upstream_response', part: 'body', operator: 'contains', value: 'invalid' },
      ],
      actions: [
        { type: 'disable', packet: 'upstream_response', target: 'api_key' },
      ],
      notes: '先用响应码缩小范围，再检查Body内容，适合状态码+错误码的组合判定。',
    },
  },
  {
    value: 'upstream-status-500-clean',
    label: '上游响应码500返回纯净500',
    rule: { name: '[运营商到CPA]响应码500直接返回纯净500', packet: 'upstream_response', part: 'status', operator: 'num_eq', value_number: 500, action: 'return_clean_500', target: 'response' },
  },
  {
    value: 'block-client-header-keyword',
    label: '[客户端到CPA]拦截Header关键字',
    rule: { name: '[客户端到CPA]拦截Header关键字', packet: 'client_request', part: 'headers', operator: 'contains', value: '关键字', action: 'block', notes: '拦截请求会中止本次请求，并向客户端返回403 JSON错误。' },
  },
  {
    value: 'block-client-header-field',
    label: '[客户端到CPA]拦截Authorization关键字',
    rule: { name: '[客户端到CPA]拦截Authorization关键字', packet: 'client_request', part: 'header', header: 'Authorization', operator: 'contains', value: '关键字', action: 'block', notes: '拦截请求会中止本次请求，并向客户端返回403 JSON错误。' },
  },
  {
    value: 'block-client-body-keyword',
    label: '[客户端到CPA]拦截Body关键字',
    rule: { name: '[客户端到CPA]拦截Body关键字', packet: 'client_request', part: 'body', operator: 'contains', value: '关键字', action: 'block', notes: 'Body字符串包含即可命中，比完整数据包通配更省CPU。' },
  },
  {
    value: 'block-client-dialog-keyword',
    label: '[客户端到CPA]拦截非法AI对话内容',
    rule: { name: '[客户端到CPA]拦截非法AI对话内容', packet: 'client_request', part: 'body_json', json_path: 'messages.#.content', operator: 'contains', value: '关键字', action: 'block', notes: '只检查messages内容，避免扫描完整HTTP包。' },
  },
  {
    value: 'block-client-ua-keyword',
    label: '[客户端到CPA]拦截异常UA',
    rule: { name: '[客户端到CPA]拦截异常UA', packet: 'client_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '关键字', action: 'block' },
  },
  {
    value: 'upstream-random-codex-ua',
    label: '[CPA到运营商]替换UA为随机Codex',
    rule: { name: '[CPA到运营商]替换UA为随机Codex', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_codex_ua}}' },
  },
  {
    value: 'upstream-random-claude-ua',
    label: '[CPA到运营商]替换UA为随机Claude Code',
    rule: { name: '[CPA到运营商]替换UA为随机Claude Code', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_claude_code_ua}}' },
  },
  {
    value: 'upstream-random-curl-ua',
    label: '[CPA到运营商]替换UA为随机curl',
    rule: { name: '[CPA到运营商]替换UA为随机curl', packet: 'upstream_request', part: 'header', header: 'User-Agent', operator: 'contains', value: '', action: 'replace', replacement: '{{random_curl_ua}}' },
  },
  {
    value: 'upstream-model-llama-test',
    label: '[CPA到运营商]替换指定模型',
    rule: { name: '[CPA到运营商]替换指定模型', packet: 'upstream_request', part: 'body_json', json_path: 'model', operator: 'equals', value: 'llama-3.1-8b-instant', action: 'replace', replacement: 'llama-3.1-8b-instant-test' },
  },
  {
    value: 'upstream-append-system',
    label: '[CPA到运营商]追加system提示词',
    rule: { name: '[CPA到运营商]追加system提示词', packet: 'upstream_request', part: 'body_json', json_path: 'messages.0.content', operator: 'contains', value: '', action: 'replace', replacement: '{original}\n新增提示词' },
  },
  {
    value: 'upstream-append-prompt',
    label: '[CPA到运营商]追加提示词片段',
    rule: { name: '[CPA到运营商]追加提示词片段', packet: 'upstream_request', part: 'body', operator: 'contains', value: '"content":"', action: 'replace', replacement: '"content":"新增提示词\\n', replace_limit: 1 },
  },
  {
    value: 'upstream-top-p-1',
    label: '[CPA到运营商]固定top_p为1',
    rule: { name: '[CPA到运营商]固定top_p为1', packet: 'upstream_request', part: 'body_json', json_path: 'top_p', operator: 'contains', value: '', action: 'replace', replacement: '1' },
  },
  {
    value: 'upstream-max-tokens-1024',
    label: '[CPA到运营商]限制max_tokens为1024',
    rule: { name: '[CPA到运营商]限制max_tokens为1024', packet: 'upstream_request', part: 'body_json', json_path: 'max_tokens', operator: 'contains', value: '', action: 'replace', replacement: '1024' },
  },
  {
    value: 'upstream-response-groq-eof-redact-message',
    label: '[运营商到CPA]脱敏Groq EOF错误',
    rule: { name: '[运营商到CPA]脱敏Groq EOF错误', provider_keyword: 'groq', packet: 'upstream_response', part: 'body_json', json_path: 'error.message', operator: 'contains', value: 'api.groq.com/openai', action: 'redact', replacement: 'Upstream provider error' },
  },
  {
    value: 'upstream-response-500-clean',
    label: '[运营商到CPA]500直接返回纯净500',
    rule: { name: '[运营商到CPA]500直接返回纯净500', packet: 'upstream_response', part: 'packet', operator: 'starts_with', value: 'HTTP/2 500', action: 'return_clean_500', target: 'response', notes: '命中后中止后续处理，客户端只看到纯净500 JSON。' },
  },
  {
    value: 'upstream-response-401-disable-key',
    label: '[运营商到CPA]401禁用API Key',
    rule: { name: '[运营商到CPA]401禁用API Key', packet: 'upstream_response', part: 'body_json', json_path: 'error.code', operator: 'contains', value: 'invalid', action: 'disable', target: 'api_key' },
  },
  {
    value: 'upstream-response-429-cooldown-key-300s',
    label: '[运营商到CPA]429冷却API Key 300s',
    rule: { name: '[运营商到CPA]429冷却API Key 300s', packet: 'upstream_response', part: 'packet', operator: 'starts_with', value: 'HTTP/2 429', action: 'cooldown', target: 'api_key', cooldown_seconds: 300 },
  },
  {
    value: 'upstream-response-5xx-cooldown-auth-60s',
    label: '[运营商到CPA]5xx冷却认证文件60s',
    rule: { name: '[运营商到CPA]5xx冷却认证文件60s', packet: 'upstream_response', part: 'packet', operator: 'starts_with', value: 'HTTP/2 5', action: 'cooldown', target: 'auth', cooldown_seconds: 60 },
  },
  {
    value: 'client-response-delete-model-header',
    label: '[CPA到客户端]删除Header模型名',
    rule: { name: '[CPA到客户端]删除Header模型名', packet: 'client_response', part: 'header', header: 'X-Model', operator: 'contains', value: '', action: 'delete', replacement: '' },
  },
  {
    value: 'client-response-redact-model-body',
    label: '[CPA到客户端]脱敏Body模型名',
    rule: { name: '[CPA到客户端]脱敏Body模型名', packet: 'client_response', part: 'body', operator: 'contains', value: 'llama-3.1-8b-instant', action: 'redact', replacement: '[model-redacted]' },
  },
  {
    value: 'client-response-groq-500-redact-message',
    label: '[CPA到客户端]脱敏Groq 500错误message',
    rule: { name: '[CPA到客户端]脱敏Groq 500错误message', provider_keyword: 'groq', packet: 'client_response', part: 'body_json', json_path: 'error.message', operator: 'contains', value: 'api.groq.com/openai', action: 'redact', replacement: 'Internal Server Error', notes: '推荐用于隐藏上游URL、网络错误、供应商内部错误细节。' },
  },
  {
    value: 'client-response-500-clean',
    label: '[CPA到客户端]脱敏返回纯净500请求',
    rule: { name: '[CPA到客户端]脱敏返回纯净500请求', packet: 'client_response', part: 'packet', operator: 'starts_with', value: 'HTTP/1.1 500', action: 'return_clean_500', target: 'response', notes: '命中后客户端只收到纯净500 JSON，不暴露原始error.message。' },
  },
  {
    value: 'client-response-429-clean',
    label: '[CPA到客户端]脱敏返回纯净429请求',
    rule: { name: '[CPA到客户端]脱敏返回纯净429请求', packet: 'client_response', part: 'packet', operator: 'starts_with', value: 'HTTP/1.1 429', action: 'return_clean_429', target: 'response' },
  },
  {
    value: 'client-response-401-clean',
    label: '[CPA到客户端]脱敏返回纯净401请求',
    rule: { name: '[CPA到客户端]脱敏返回纯净401请求', packet: 'client_response', part: 'packet', operator: 'starts_with', value: 'HTTP/1.1 401', action: 'return_clean_401', target: 'response' },
  },
  {
    value: 'disable-key-org-restricted',
    label: '[运营商到CPA]400 organization_restricted禁用Key',
    rule: { name: '[运营商到CPA]400 organization_restricted禁用API Key', packet: 'upstream_response', part: 'body_json', json_path: 'error.code', operator: 'equals', value: 'organization_restricted', action: 'disable', target: 'api_key' },
  },
  {
    value: 'disable-key-wrong-api-key',
    label: '[运营商到CPA]401 wrong_api_key禁用Key',
    rule: { name: '[运营商到CPA]401 wrong_api_key禁用API Key', packet: 'upstream_response', part: 'body_json', json_path: 'error.code', operator: 'equals', value: 'wrong_api_key', action: 'disable', target: 'api_key' },
  },
  {
    value: 'disable-key-unauthorized-body',
    label: '[运营商到CPA]401 unauthorized禁用Key',
    rule: { name: '[运营商到CPA]401 unauthorized禁用API Key', packet: 'upstream_response', part: 'body', operator: 'equals', value: 'unauthorized', action: 'disable', target: 'api_key' },
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

const parseCooldownSeconds = (value: string) => {
  const raw = value.trim().toLowerCase();
  const match = raw.match(/^(\d+)\s*([shd])?$/);
  if (!match) return 0;
  const amount = Number(match[1]);
  if (!Number.isFinite(amount)) return 0;
  if (match[2] === 'h') return amount * 3600;
  if (match[2] === 'd') return amount * 86400;
  return amount;
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

const defaultCondition = (packet = 'client_request'): PacketRuleCondition => ({
  packet,
  part: 'body',
  operator: 'contains',
  value: '',
});

const defaultAction = (packet = 'client_request'): PacketRuleAction => ({
  type: 'record',
  packet,
  part: 'body',
});

const compactRuleSummary = (rule: PacketRule) => {
  const conditions = rule.conditions?.length
    ? `${rule.match_logic === 'any' ? '任一' : '全部'}条件 ${rule.conditions.length} 个`
    : `${rule.packet} · ${rule.part} · ${rule.operator}`;
  const actions = rule.actions?.length
    ? `动作 ${rule.actions.map((item) => item.type).join(', ')}`
    : rule.action;
  return `${conditions} · ${actions}`;
};

export function PacketCapturePage() {
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const config = useConfigStore((state) => state.config);
  const importInputRef = useRef<HTMLInputElement | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [cliDetailedLog, setCliDetailedLog] = useState(true);
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
    setCliDetailedLog(Boolean(state['cli-detailed-log']));
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
    const firstCondition = editingRule.conditions?.[0];
    const firstAction = editingRule.actions?.[0];
    await packetCaptureApi.saveRule({
      ...editingRule,
      packet: firstCondition?.packet || editingRule.packet || 'client_request',
      part: firstCondition?.part || editingRule.part || 'body',
      json_path: firstCondition?.json_path ?? editingRule.json_path,
      header: firstCondition?.header ?? editingRule.header,
      operator: firstCondition?.operator || editingRule.operator || 'contains',
      value: firstCondition?.value ?? editingRule.value,
      value_number: firstCondition?.value_number ?? editingRule.value_number,
      action: firstAction?.type || editingRule.action || 'record',
      replacement: firstAction?.replacement ?? editingRule.replacement,
      replace_limit: firstAction?.replace_limit ?? editingRule.replace_limit,
      cooldown_seconds: firstAction?.cooldown_seconds ?? editingRule.cooldown_seconds,
      target: firstAction?.target ?? editingRule.target,
    });
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

  const updateCondition = (index: number, patch: Partial<PacketRuleCondition>) => {
    if (!editingRule) return;
    const conditions = [...(editingRule.conditions?.length ? editingRule.conditions : [defaultCondition(editingRule.packet)])];
    conditions[index] = { ...conditions[index], ...patch };
    setEditingRule({ ...editingRule, conditions });
  };

  const updateAction = (index: number, patch: Partial<PacketRuleAction>) => {
    if (!editingRule) return;
    const actions = [...(editingRule.actions?.length ? editingRule.actions : [defaultAction(editingRule.packet)])];
    actions[index] = { ...actions[index], ...patch };
    setEditingRule({ ...editingRule, actions });
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
      conditions: template.rule.conditions || [
        {
          packet: template.rule.packet || editingRule.packet,
          part: template.rule.part || editingRule.part,
          json_path: template.rule.json_path,
          header: template.rule.header,
          operator: template.rule.operator || editingRule.operator,
          value: template.rule.value,
          value_number: template.rule.value_number,
        },
      ],
      actions: template.rule.actions || [
        {
          type: template.rule.action || editingRule.action,
          packet: template.rule.packet || editingRule.packet,
          part: template.rule.part || editingRule.part,
          json_path: template.rule.json_path,
          header: template.rule.header,
          value: template.rule.value,
          replacement: template.rule.replacement,
          replace_limit: template.rule.replace_limit,
          target: template.rule.target,
          cooldown_seconds: template.rule.cooldown_seconds,
        },
      ],
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
                  const state = await packetCaptureApi.setState({ enabled: next });
                  setEnabled(Boolean(state.enabled));
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
        title={
          <div className={styles.captureTitle}>
            <span>CPA命令行日志</span>
            <label className={styles.switch}>
              <input
                type="checkbox"
                checked={cliDetailedLog}
                onChange={async (event) => {
                  const next = event.target.checked;
                  const state = await packetCaptureApi.setState({ 'cli-detailed-log': next });
                  setCliDetailedLog(Boolean(state['cli-detailed-log']));
                }}
              />
              <span className={styles.switchTrack} aria-hidden="true" />
              <span className={styles.switchText}>CPA命令行是否显示详细数据包[1~6]记录</span>
            </label>
          </div>
        }
      >
        <p>控制是否在命令行输出完整调试数据包日志，范围包含“CPA收到客户端请求”至“CPA发送给客户端”。默认开启，配置保存在 `config.yaml` 的 `packet-capture.cli-detailed-log`。</p>
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
                <span>{rule.record_history ?? true ? '记录触发历史' : '不记录触发历史'} · {compactRuleSummary(rule)}</span>
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
            <label>匹配逻辑<Select value={editingRule.match_logic || 'all'} options={[{ value: 'all', label: '全部条件都满足' }, { value: 'any', label: '任一条件满足' }]} onChange={(value) => setEditingRule({ ...editingRule, match_logic: value })} ariaLabel="匹配逻辑" /></label>
            <div className={styles.full}>
              <div className={styles.ruleSectionHeader}>
                <strong>匹配条件</strong>
                <Button size="sm" variant="secondary" onClick={() => setEditingRule({ ...editingRule, conditions: [...(editingRule.conditions || []), defaultCondition(editingRule.packet)] })}>添加条件</Button>
              </div>
              <div className={styles.ruleSubGrid}>
                {(editingRule.conditions?.length ? editingRule.conditions : [defaultCondition(editingRule.packet)]).map((condition, index) => (
                  <div className={styles.ruleSubItem} key={`condition-${index}`}>
                    <Select value={condition.packet || editingRule.packet} options={packetOptions} onChange={(value) => updateCondition(index, { packet: value })} ariaLabel="数据包" />
                    <Select value={condition.part || 'body'} options={partOptions} onChange={(value) => updateCondition(index, { part: value })} ariaLabel="位置" />
                    <SuggestInput value={condition.header || ''} options={headerOptions} onChange={(value) => updateCondition(index, { header: value })} placeholder="Header" />
                    <Input value={condition.json_path || ''} onChange={(event) => updateCondition(index, { json_path: event.target.value })} placeholder="JSON路径" />
                    <Select value={condition.operator || 'contains'} options={operatorOptions} onChange={(value) => updateCondition(index, { operator: value })} ariaLabel="判断" />
                    <Input value={condition.value || ''} onChange={(event) => updateCondition(index, { value: event.target.value })} placeholder="匹配值" />
                    <Input value={String(condition.value_number || 0)} onChange={(event) => updateCondition(index, { value_number: Number(event.target.value) || 0 })} placeholder="数值" />
                    <Button size="sm" variant="secondary" onClick={() => setEditingRule({ ...editingRule, conditions: (editingRule.conditions || []).filter((_, i) => i !== index) })}>删除</Button>
                  </div>
                ))}
              </div>
            </div>
            <div className={styles.full}>
              <div className={styles.ruleSectionHeader}>
                <strong>执行动作</strong>
                <Button size="sm" variant="secondary" onClick={() => setEditingRule({ ...editingRule, actions: [...(editingRule.actions || []), defaultAction(editingRule.packet)] })}>添加动作</Button>
              </div>
              <div className={styles.ruleSubGrid}>
                {(editingRule.actions?.length ? editingRule.actions : [defaultAction(editingRule.packet)]).map((action, index) => (
                  <div className={styles.ruleSubItem} key={`action-${index}`}>
                    <Select value={action.type || 'record'} options={actionOptions.map(({ value, label }) => ({ value, label }))} onChange={(value) => updateAction(index, { type: value })} ariaLabel="动作" />
                    <Select value={action.packet || editingRule.packet} options={packetOptions} onChange={(value) => updateAction(index, { packet: value })} ariaLabel="数据包" />
                    <Select value={action.part || 'body'} options={partOptions} onChange={(value) => updateAction(index, { part: value })} ariaLabel="位置" />
                    <SuggestInput value={action.header || ''} options={headerOptions} onChange={(value) => updateAction(index, { header: value })} placeholder="Header" />
                    <Input value={action.json_path || ''} onChange={(event) => updateAction(index, { json_path: event.target.value })} placeholder="JSON路径" />
                    <SuggestInput value={action.replacement || ''} options={replacementOptions} onChange={(value) => updateAction(index, { replacement: value })} placeholder="替换/追加内容" />
                    <Input value={String(action.replace_limit || 0)} onChange={(event) => updateAction(index, { replace_limit: Number(event.target.value) || 0 })} placeholder="次数" />
                    <SuggestInput value={action.target || ''} options={targetOptions} onChange={(value) => updateAction(index, { target: value })} placeholder="目标" />
                    <Input value={String(action.cooldown_seconds || 0)} onChange={(event) => updateAction(index, { cooldown_seconds: parseCooldownSeconds(event.target.value) })} placeholder="冷却" />
                    <Button size="sm" variant="secondary" onClick={() => setEditingRule({ ...editingRule, actions: (editingRule.actions || []).filter((_, i) => i !== index) })}>删除</Button>
                  </div>
                ))}
              </div>
            </div>
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
            <label>冷却时长<Input value={String(editingRule.cooldown_seconds || 0)} onChange={(event) => setEditingRule({ ...editingRule, cooldown_seconds: parseCooldownSeconds(event.target.value) })} placeholder="300 / 30s / 2h / 1d" /></label>
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
