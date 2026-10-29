import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import {
  Folder, FileText, Database, ChevronRight, ChevronDown, ArrowLeft, Upload, Trash2, HardDrive,
  X, Activity, Users, Cpu, Search, Boxes, AlertTriangle
} from 'lucide-react';

// --- 分類 ---

// 監視対象外: 監視サービス自身（mon-*）と、ビルド後に終了するビルダー（*-builder）
const isSystemContainer = (name) => (name || '').startsWith('mon-') || /-builder$/.test(name);

// compose のサービス名（ラベルが無いコンテナは名前から atmosidea- を外したもの）
const serviceName = (c) => c.service || c.name.replace(/^atmosidea-/i, '');

// レイヤー（縦の層）。サービス名の接尾辞で判定する
const LAYERS = [
  { key: 'frontend', label: 'Frontend' },
  { key: 'api', label: 'API' },
  { key: 'upload', label: 'Upload' },
  { key: 'worker', label: 'Worker' },
  { key: 'db', label: 'DB/Cache' },
  { key: 'storage', label: 'Storage' },
  { key: 'infra', label: 'Init/Infra' },
  { key: 'other', label: 'その他' },
];
const LAYER_INDEX = Object.fromEntries(LAYERS.map((l, i) => [l.key, i]));
const layerLabel = (key) => LAYERS[LAYER_INDEX[key]]?.label ?? key;

const getLayer = (c) => {
  const s = serviceName(c).toLowerCase();
  if (s === 'frontend' || s.endsWith('-frontend')) return 'frontend';
  if (s.endsWith('upload-api')) return 'upload';
  if (s.endsWith('-service')) return 'api';
  if (s.endsWith('-init') || s.includes('docker-proxy')) return 'infra';
  if (s.endsWith('-worker') || s === 'sfsp-api' || s.includes('clamav') || s.includes('yara')) return 'worker';
  if (s.endsWith('-db') || s === 'redis') return 'db';
  if (s.includes('minio') || s.includes('storage')) return 'storage';
  return 'other';
};

// 機能（サービス群）。サービス名の接頭辞で判定し、どれにも当たらないもの（frontend・minio・redis・app-db・init 系）は共通基盤
const FEATURES = ['auth', 'profile', 'team', 'mypage', 'game', 'video', 'static-site', 'sfsp'];
const SHARED_FEATURE = '共通基盤';
const FEATURE_INDEX = Object.fromEntries([...FEATURES, SHARED_FEATURE].map((f, i) => [f, i]));

const getFeature = (c) => {
  const s = serviceName(c).toLowerCase();
  return FEATURES.find((f) => s === f || s.startsWith(`${f}-`)) ?? SHARED_FEATURE;
};

// ツリーのグループ分け。グループにしなかった方の軸は各行のバッジで示す
const GROUP_MODES = {
  feature: { label: '機能 › レイヤー', keyOf: getFeature, groupIndex: FEATURE_INDEX, groupLabel: (k) => k, badgeOf: (c) => layerLabel(getLayer(c)), childIndex: (c) => LAYER_INDEX[getLayer(c)] },
  layer: { label: 'レイヤー › 機能', keyOf: getLayer, groupIndex: LAYER_INDEX, groupLabel: layerLabel, badgeOf: getFeature, childIndex: (c) => FEATURE_INDEX[getFeature(c)] },
};

const NO_PROJECT = '(compose 外)';

// --- 状態 ---

// exit 0 で終わった一回きりのコンテナ（init 系など）。異常ではない
const isCompleted = (c) => c.state === 'exited' && /\(0\)/.test(c.status || '');

const STATUS_STYLE = {
  error: { dot: 'bg-rose-500 animate-pulse', text: 'text-rose-400' },
  down: { dot: 'bg-rose-500', text: 'text-rose-400' },
  warn: { dot: 'bg-amber-400', text: 'text-amber-300' },
  ok: { dot: 'bg-emerald-500', text: 'text-emerald-400' },
  done: { dot: 'border border-slate-500', text: 'text-slate-500' },
};
const SEVERITY = { error: 4, down: 3, warn: 2, ok: 1, done: 0 };

// error: unhealthy またはエラーログ多発 / down: 異常終了 / warn: 起動中・一時停止など / ok: 稼働中 / done: 正常終了
const getStatus = (c, hasError) => {
  if (hasError || c.health === 'unhealthy') return 'error';
  if (c.state === 'running') return c.health === 'starting' ? 'warn' : 'ok';
  if (isCompleted(c)) return 'done';
  if (c.state === 'exited' || c.state === 'dead') return 'down';
  return 'warn';
};

const worstStatus = (statuses) => statuses.reduce((w, s) => (SEVERITY[s] > SEVERITY[w] ? s : w), 'done');

const ERROR_LINE = /(error|fatal|fail|exception|stderr|panic)/i;
// エラー検出後、赤い強調表示を保持してから状態をリセットするまでの時間（ms）
const ERROR_HIGHLIGHT_MS = 10000;
const MAX_LOG_LINES = 500;

const formatBytes = (b) => {
  if (!b) return '-';
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(1)}GB`;
  return `${Math.round(b / 1024 ** 2)}MB`;
};

// --- 小さな部品 ---

const StatusDot = ({ status }) => (
  <span className={`inline-block w-2 h-2 rounded-full shrink-0 ${STATUS_STYLE[status].dot}`} />
);

const Metric = ({ icon: Icon, label, value, sub, title, children }) => (
  <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-slate-900/70 border border-slate-800" title={title}>
    <Icon size={14} className="text-cyan-400 shrink-0" />
    <span className="text-[11px] text-slate-500">{label}</span>
    <span className="font-mono text-sm text-slate-100">{value}</span>
    {sub && <span className="text-[11px] text-slate-500">{sub}</span>}
    {children}
  </div>
);

const Sparkline = ({ values }) => {
  const max = Math.max(1, ...values);
  const points = values.map((v, i) => `${(i / (values.length - 1)) * 60},${18 - (v / max) * 16}`).join(' ');
  return (
    <svg width="60" height="18" className="shrink-0">
      <polyline points={points} fill="none" stroke="rgba(34, 211, 238, 0.8)" strokeWidth="1.5" />
    </svg>
  );
};

// --- ツリー ---

const GroupRow = ({ label, depth, open, onToggle, status, running, total, strong }) => (
  <div
    onClick={onToggle}
    className="flex items-center gap-1.5 h-7 pr-3 cursor-pointer hover:bg-slate-800/60 text-[13px] select-none"
    style={{ paddingLeft: 8 + depth * 16 }}
  >
    {open ? <ChevronDown size={14} className="text-slate-500 shrink-0" /> : <ChevronRight size={14} className="text-slate-500 shrink-0" />}
    <StatusDot status={status} />
    <span className={`truncate ${strong ? 'font-semibold text-slate-100' : 'text-slate-300'}`}>{label}</span>
    <span className="ml-auto font-mono text-[11px] text-slate-500 shrink-0" title="稼働中 / 全体">{running}/{total}</span>
  </div>
);

const ContainerRow = ({ c, depth, status, badge, stats, isActive, isSelected, onSelect }) => (
  <div
    onClick={() => onSelect(c.name)}
    title={`${c.name}\n${c.image}\n${c.status}`}
    className={`flex items-center gap-2 h-7 pr-3 cursor-pointer text-[13px] select-none ${
      isSelected ? 'bg-cyan-500/15 text-cyan-100' : status === 'error' ? 'bg-rose-500/10 hover:bg-rose-500/20' : 'hover:bg-slate-800/60'
    }`}
    style={{ paddingLeft: 8 + depth * 16 + 20 }}
  >
    <StatusDot status={status} />
    <span className={`truncate ${status === 'done' ? 'text-slate-500' : ''}`}>{serviceName(c)}</span>
    {badge && <span className="shrink-0 text-[10px] px-1.5 py-px rounded bg-slate-800 text-slate-400">{badge}</span>}
    <span className="ml-auto flex items-center gap-2 font-mono text-[11px] text-slate-500 shrink-0">
      <span className={`w-1.5 h-1.5 rounded-full transition-colors ${isActive ? 'bg-cyan-400' : 'bg-transparent'}`} title="ログ出力中" />
      <span className="w-9 text-right">{stats ? `${stats.cpuPercent.toFixed(0)}%` : ''}</span>
      <span className="w-12 text-right">{stats ? formatBytes(stats.memUsage) : ''}</span>
    </span>
  </div>
);

// --- 右ペイン: 概要（未選択時） ---

const Overview = ({ project, counts, issues, statusOf, onSelect }) => (
  <div className="flex-1 overflow-y-auto p-6 space-y-6">
    <div>
      <h2 className="text-lg font-semibold text-slate-100">{project || 'コンテナ'}</h2>
      <p className="text-xs text-slate-500 mt-1">左のツリーからコンテナを選ぶと、ログ・リソース・依存関係を表示します。</p>
    </div>
    <div className="grid grid-cols-2 xl:grid-cols-4 gap-3">
      {[
        { label: '稼働中', value: counts.ok + counts.warn, status: 'ok' },
        { label: '正常終了（init 等）', value: counts.done, status: 'done' },
        { label: '異常終了・停止', value: counts.down, status: 'down' },
        { label: '異常（unhealthy・エラー多発）', value: counts.error, status: 'error' },
      ].map((t) => (
        <div key={t.label} className="rounded-lg border border-slate-800 bg-slate-900/50 p-4">
          <div className="flex items-center gap-2 text-xs text-slate-400"><StatusDot status={t.status} />{t.label}</div>
          <div className={`mt-2 text-3xl font-mono ${t.value > 0 && (t.status === 'down' || t.status === 'error') ? 'text-rose-400' : 'text-slate-100'}`}>{t.value}</div>
        </div>
      ))}
    </div>
    <div>
      <h3 className="flex items-center gap-2 text-sm font-semibold text-slate-200 mb-2"><AlertTriangle size={15} className="text-amber-400" />要確認</h3>
      {issues.length === 0 ? (
        <div className="text-sm text-slate-500 border border-slate-800 rounded-lg p-4">問題は検出されていません。</div>
      ) : (
        <div className="border border-slate-800 rounded-lg divide-y divide-slate-800">
          {issues.map(({ c, reasons }) => (
            <div key={c.name} onClick={() => onSelect(c.name)} className="flex items-center gap-3 px-4 py-2.5 cursor-pointer hover:bg-slate-800/50">
              <StatusDot status={statusOf(c)} />
              <span className="text-sm text-slate-100 w-56 truncate">{serviceName(c)}</span>
              <span className="text-xs text-slate-400 truncate">{reasons.join(' / ')}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  </div>
);

// --- 右ペイン: コンテナ詳細 ---

const Field = ({ label, children, className = 'text-slate-200' }) => (
  <div className="flex items-baseline gap-1.5">
    <span className="text-slate-500">{label}</span>
    <span className={`font-mono ${className}`}>{children}</span>
  </div>
);

const ChipRow = ({ label, children }) => (
  <div className="flex items-start gap-2 text-xs">
    <span className="text-slate-500 w-20 shrink-0 pt-0.5">{label}</span>
    <div className="flex flex-wrap gap-1.5">{children}</div>
  </div>
);

const DepChips = ({ list, statusOf, onSelect }) =>
  list.length === 0 ? <span className="text-slate-600">なし</span> : list.map((d) => (
    <button key={d.name} onClick={() => onSelect(d.name)} className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-200">
      <StatusDot status={statusOf(d)} />{serviceName(d)}
    </button>
  ));

const DetailPane = ({ c, status, stats, networks, deps, rdeps, statusOf, onSelect, onClose, activeTab, setActiveTab, canShowStorage, logs, storageProps }) => {
  const logBoxRef = useRef(null);
  const tab = activeTab === 'storage' && canShowStorage ? 'storage' : 'logs';

  // 新しいログが来たら末尾までスクロールする
  useEffect(() => {
    const el = logBoxRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [logs, tab, c.name]);

  const memPct = stats?.memLimit ? (stats.memUsage / stats.memLimit) * 100 : 0;

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div className="px-5 py-4 border-b border-slate-800 space-y-3 shrink-0">
        <div className="flex items-start gap-3">
          <span className="pt-2"><StatusDot status={status} /></span>
          <div className="min-w-0">
            <h2 className="text-lg font-semibold text-slate-100 truncate">{serviceName(c)}</h2>
            <div className="text-xs text-slate-500 font-mono truncate">{c.name} · {c.image}</div>
          </div>
          <button onClick={onClose} title="概要に戻る" className="ml-auto p-1.5 rounded text-slate-400 hover:text-white hover:bg-slate-800"><X size={18} /></button>
        </div>
        <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs">
          <Field label="状態" className={STATUS_STYLE[status].text}>{c.status}</Field>
          {c.health && <Field label="ヘルス" className={c.health === 'unhealthy' ? 'text-rose-400' : 'text-slate-200'}>{c.health}</Field>}
          <Field label="稼働時間">{c.state === 'running' && c.uptime ? c.uptime : '-'}</Field>
          <Field label="再起動" className={c.restartCount > 0 ? 'text-amber-300' : 'text-slate-200'}>{c.restartCount ?? 0} 回</Field>
          <Field label="CPU">{stats ? `${stats.cpuPercent.toFixed(1)}%` : '-'}</Field>
          <Field label="メモリ">{stats ? `${formatBytes(stats.memUsage)} (${memPct.toFixed(1)}%)` : '-'}</Field>
        </div>
        {networks.length > 0 && (
          <ChipRow label="ネットワーク">
            {networks.map((n) => <span key={n} className="px-2 py-0.5 rounded bg-slate-800/70 text-slate-300 font-mono">{n}</span>)}
          </ChipRow>
        )}
        {c.project && (
          <>
            <ChipRow label="依存先"><DepChips list={deps} statusOf={statusOf} onSelect={onSelect} /></ChipRow>
            <ChipRow label="依存元"><DepChips list={rdeps} statusOf={statusOf} onSelect={onSelect} /></ChipRow>
          </>
        )}
      </div>

      <div className="flex items-center gap-1 px-5 pt-3 shrink-0 text-xs">
        {['logs', ...(canShowStorage ? ['storage'] : [])].map((t) => (
          <button key={t} onClick={() => setActiveTab(t)} className={`px-3 py-1.5 rounded-md ${tab === t ? 'bg-cyan-600 text-white' : 'text-slate-400 hover:bg-slate-800'}`}>
            {t === 'logs' ? 'ログ' : 'Storage'}
          </button>
        ))}
      </div>
      <div className="flex-1 min-h-0 p-5 pt-3">
        {tab === 'storage' ? <StorageExplorer {...storageProps} /> : (
          <div ref={logBoxRef} className="h-full overflow-y-auto bg-black/40 border border-slate-800 rounded-lg p-3 font-mono text-xs space-y-0.5 select-text">
            {logs.length === 0 ? (
              <div className="text-slate-600 italic">ログを待っています…</div>
            ) : logs.map((log, i) => (
              <div key={i} className={`whitespace-pre-wrap break-all ${ERROR_LINE.test(log) ? 'text-rose-400' : 'text-slate-300'}`}>{log}</div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

const StorageExplorer = ({ loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete }) => (
  <div className="h-full bg-slate-950/50 border border-slate-800 rounded-lg flex flex-col overflow-hidden">
    <div className="p-2 border-b border-slate-800 bg-slate-900/50 flex items-center justify-between gap-2">
      <div className="flex items-center gap-2">
        <button onClick={handleGoBack} disabled={!currentBucket && !currentPrefix} className="p-1.5 hover:bg-slate-800 rounded disabled:opacity-30 text-slate-300"><ArrowLeft className="w-4 h-4" /></button>
        <div className="flex items-center gap-1 bg-slate-900 border border-slate-800 px-3 py-1 rounded text-xs text-slate-300 font-mono">
          <span className="text-blue-400">Root</span>
          {currentBucket && <><ChevronRight className="w-3 h-3 text-slate-600" /><span className="text-blue-400">{currentBucket}</span></>}
          {currentPrefix.split('/').filter(Boolean).map((p, i) => <React.Fragment key={i}><ChevronRight className="w-3 h-3 text-slate-600" /><span>{p}</span></React.Fragment>)}
        </div>
      </div>
      {currentBucket && (
        <div className="flex items-center gap-2">
          <input type="file" ref={fileInputRef} onChange={handleUpload} className="hidden" />
          <button onClick={() => fileInputRef.current?.click()} className="flex items-center gap-1.5 text-xs bg-blue-600 text-white px-3 py-1.5 rounded font-medium"><Upload className="w-3.5 h-3.5" /> Upload</button>
          <button onClick={handleDelete} disabled={!selectedItem || selectedItem.type !== 'file'} className="flex items-center gap-1.5 text-xs bg-rose-600/20 border border-rose-500/30 text-rose-400 px-3 py-1.5 rounded disabled:opacity-30"><Trash2 className="w-3.5 h-3.5" /> Delete</button>
        </div>
      )}
    </div>
    <div className="flex-1 overflow-y-auto">
      <table className="w-full text-left text-xs">
        <thead><tr className="border-b border-slate-800 text-slate-500 bg-slate-900/30 sticky top-0"><th className="p-2.5 pl-4 font-medium">Name</th><th className="p-2.5 font-medium w-24">Type</th><th className="p-2.5 font-medium w-28 text-right pr-4">Size</th></tr></thead>
        <tbody>
          {loading ? <tr><td colSpan="3" className="p-8 text-center text-slate-600">Loading...</td></tr> : items.length === 0 ? <tr><td colSpan="3" className="p-8 text-center text-slate-600">Empty</td></tr> : items.map((item, index) => <tr key={index} onClick={() => setSelectedItem(item)} onDoubleClick={() => handleDoubleClick(item)} className={`border-b border-slate-800/40 cursor-pointer ${selectedItem?.name === item.name ? 'bg-blue-600/30' : 'hover:bg-slate-900/80'}`}><td className="p-2 pl-4 flex items-center gap-2 font-medium">{item.type === 'bucket' ? <Database className="w-4 h-4 text-emerald-400" /> : item.type === 'folder' ? <Folder className="w-4 h-4 text-amber-400" /> : <FileText className="w-4 h-4 text-blue-400" />}<span>{item.name}</span></td><td className="p-2 capitalize text-slate-500">{item.type}</td><td className="p-2 pr-4 text-right font-mono text-slate-500">{item.size ? `${(item.size / 1024).toFixed(1)} KB` : '-'}</td></tr>)}
        </tbody>
      </table>
    </div>
  </div>
);

const VolumesModal = ({ onClose, volumes }) => (
  <div className="fixed inset-0 bg-black/60 backdrop-blur-sm z-30 flex items-center justify-center p-4">
    <div className="w-full max-w-2xl bg-slate-950 border border-slate-700 rounded-xl shadow-2xl flex flex-col">
      <div className="p-4 border-b border-slate-800 flex items-center justify-between">
        <h1 className="font-semibold text-slate-100 flex items-center gap-2"><HardDrive className="w-5 h-5 text-cyan-400" />Volumes</h1>
        <button onClick={onClose} className="p-2 text-slate-400 hover:text-white"><X className="w-5 h-5" /></button>
      </div>
      <div className="p-4 overflow-y-auto max-h-[60vh]">
        {volumes.length === 0 ? <div className="text-slate-600 italic text-center py-6">Loading...</div> : (
          <table className="w-full text-left text-xs font-mono">
            <thead><tr className="border-b border-slate-800 text-slate-500"><th className="p-2">Name</th><th className="p-2">Driver</th><th className="p-2 text-right">Mountpoint</th></tr></thead>
            <tbody>
              {volumes.map((v, i) => (
                <tr key={i} className="border-b border-slate-800/40 hover:bg-slate-900/40">
                  <td className="p-2 text-slate-200">{v.name}</td>
                  <td className="p-2 text-slate-400">{v.driver}</td>
                  <td className="p-2 text-slate-500 text-right truncate max-w-xs" title={v.mountpoint}>{v.mountpoint}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  </div>
);

// --- Main App Component ---
export default function App() {
  const [containers, setContainers] = useState([]);
  const [containerStats, setContainerStats] = useState({});
  const [topology, setTopology] = useState([]);
  const [volumes, setVolumes] = useState([]);
  const [showVolumes, setShowVolumes] = useState(false);
  const [activeUsers, setActiveUsers] = useState(0);

  const [groupBy, setGroupBy] = useState(() => (localStorage.getItem('mon.groupBy') === 'layer' ? 'layer' : 'feature'));
  const [query, setQuery] = useState('');
  const [showCompleted, setShowCompleted] = useState(false);
  const [openMap, setOpenMap] = useState({});
  const [selectedName, setSelectedName] = useState(null);
  const [activeTab, setActiveTab] = useState('logs');

  const [logsMap, setLogsMap] = useState({});
  const [activeLogsMap, setActiveLogsMap] = useState({});
  const [errorLoopState, setErrorLoopState] = useState({});
  const [hasStorageMap, setHasStorageMap] = useState({});
  const [lps, setLps] = useState(0);
  const [lpsHistory, setLpsHistory] = useState(new Array(30).fill(0));

  const [items, setItems] = useState([]);
  const [selectedItem, setSelectedItem] = useState(null);
  const [loading, setLoading] = useState(false);
  const [currentBucket, setCurrentBucket] = useState('');
  const [currentPrefix, setCurrentPrefix] = useState('');

  const wsMapRef = useRef({});
  const logBufferRef = useRef({});
  const lastActivityRef = useRef({});
  const logCountRef = useRef(0);
  const hasStorageRef = useRef({});
  const stoppedLoadedRef = useRef({});
  const errorClearTimeoutsRef = useRef({});
  const fileInputRef = useRef(null);

  useEffect(() => { localStorage.setItem('mon.groupBy', groupBy); }, [groupBy]);

  const fetchJson = useCallback(async (url, apply) => {
    try {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
      apply(await res.json());
    } catch (err) {
      console.error(`Fetch ${url} error:`, err);
    }
  }, []);

  const fetchContainers = useCallback(() => fetchJson('/api/containers', (d) => setContainers(Array.isArray(d) ? d : [])), [fetchJson]);
  const fetchContainerStats = useCallback(() => fetchJson('/api/containers/stats', (d) => setContainerStats(d || {})), [fetchJson]);
  const fetchActiveUsers = useCallback(() => fetchJson('/api/connections/count', (d) => setActiveUsers(d.count)), [fetchJson]);
  const fetchTopology = useCallback(() => fetchJson('/api/topology', (d) => setTopology(Array.isArray(d) ? d : [])), [fetchJson]);
  const fetchVolumes = useCallback(() => fetchJson('/api/volumes', (d) => setVolumes(Array.isArray(d) ? d : [])), [fetchJson]);

  useEffect(() => {
    fetchContainers();
    fetchActiveUsers();
    fetchContainerStats();
    fetchTopology();
    fetchVolumes();
    const containerInterval = setInterval(fetchContainers, 3000);
    const statsInterval = setInterval(fetchContainerStats, 3000);
    const userInterval = setInterval(fetchActiveUsers, 10000);
    // コンテナの追加・削除やネットワーク・ボリュームの変化を反映するため、トポロジーとボリュームも定期的に取り直す
    const topologyInterval = setInterval(fetchTopology, 15000);
    const volumeInterval = setInterval(fetchVolumes, 15000);
    return () => {
      clearInterval(containerInterval);
      clearInterval(statsInterval);
      clearInterval(userInterval);
      clearInterval(topologyInterval);
      clearInterval(volumeInterval);
    };
  }, [fetchContainers, fetchActiveUsers, fetchContainerStats, fetchTopology, fetchVolumes]);

  // 自プロジェクト: atmosidea-frontend が属する compose プロジェクト。見つからなければコンテナ数が最多のプロジェクト
  const ownProject = useMemo(() => {
    const fe = containers.find((c) => c.name === 'atmosidea-frontend');
    if (fe?.project) return fe.project;
    const counts = {};
    containers.forEach((c) => { if (c.project && !isSystemContainer(c.name)) counts[c.project] = (counts[c.project] || 0) + 1; });
    return Object.entries(counts).sort((a, b) => b[1] - a[1])[0]?.[0] ?? null;
  }, [containers]);

  // LPS（2 秒ごとの集計）
  useEffect(() => {
    const lpsInterval = setInterval(() => {
      const v = logCountRef.current / 2;
      logCountRef.current = 0;
      setLps(v);
      setLpsHistory((prev) => [...prev.slice(1), v]);
    }, 2000);
    return () => clearInterval(lpsInterval);
  }, []);

  // 受信したログは一旦バッファに溜め、まとめて state に反映する（1 行ごとの再描画を避ける）
  useEffect(() => {
    const flush = setInterval(() => {
      const pending = logBufferRef.current;
      const names = Object.keys(pending);
      if (names.length > 0) {
        logBufferRef.current = {};
        setLogsMap((prev) => {
          const next = { ...prev };
          names.forEach((n) => { next[n] = [...(prev[n] || []), ...pending[n]].slice(-MAX_LOG_LINES); });
          return next;
        });
      }
      const now = Date.now();
      setActiveLogsMap((prev) => {
        const next = {};
        Object.entries(lastActivityRef.current).forEach(([n, t]) => { if (now - t < 800) next[n] = true; });
        const prevKeys = Object.keys(prev);
        const same = prevKeys.length === Object.keys(next).length && prevKeys.every((k) => next[k]);
        return same ? prev : next;
      });
    }, 300);
    return () => clearInterval(flush);
  }, []);

  // ログの WebSocket: 自プロジェクトの稼働中コンテナは常時、それ以外は選択中のときだけ接続する。
  // 停止中のコンテナは選択時に一度だけ接続して既存のログを読む（読み終わるとサーバー側が閉じる）
  useEffect(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const isLive = (c) => c.state === 'running' || c.state === 'starting' || c.state === 'restarting';
    const wanted = new Set();

    containers.forEach((c) => {
      if (isSystemContainer(c.name)) return;
      const live = isLive(c);
      if (live) delete stoppedLoadedRef.current[c.name];
      const want = live
        ? (c.project === ownProject || c.name === selectedName)
        : (c.name === selectedName && !stoppedLoadedRef.current[c.name]);
      if (!want) return;
      wanted.add(c.name);
      const existing = wsMapRef.current[c.name];
      if (existing && (existing.readyState === WebSocket.OPEN || existing.readyState === WebSocket.CONNECTING)) return;
      if (!live) stoppedLoadedRef.current[c.name] = true;

      const socket = new WebSocket(`${protocol}//${window.location.host}/ws/logs?container=${encodeURIComponent(c.name)}`);
      socket.onmessage = (event) => {
        const logData = event.data;
        logCountRef.current += 1;
        lastActivityRef.current[c.name] = Date.now();
        (logBufferRef.current[c.name] ||= []).push(logData);

        if (!hasStorageRef.current[c.name] && logData.includes('min.io')) {
          hasStorageRef.current[c.name] = true;
          setHasStorageMap((prev) => ({ ...prev, [c.name]: true }));
        }
        if (ERROR_LINE.test(logData)) {
          setErrorLoopState((prev) => ({ ...prev, [c.name]: [...(prev[c.name] || []), Date.now()].slice(-5) }));
        }
      };
      // 切断時はマップから外すだけ。再接続は containers の定期更新（3秒ごと）でこの effect が再実行されたときに行われる
      socket.onclose = () => { if (wsMapRef.current[c.name] === socket) delete wsMapRef.current[c.name]; };
      socket.onerror = (err) => { console.error(`WebSocket Error [${c.name}]:`, err); socket.close(); };
      wsMapRef.current[c.name] = socket;
    });

    // 不要になった接続を閉じる（停止中のコンテナのログ読み込み用接続は、読み終わるまで閉じない）
    Object.keys(wsMapRef.current).forEach((name) => {
      if (!wanted.has(name) && !stoppedLoadedRef.current[name]) {
        wsMapRef.current[name]?.close();
        delete wsMapRef.current[name];
      }
    });
  }, [containers, ownProject, selectedName]);

  const errorLogsMap = useMemo(() => {
    const newErrorMap = {};
    const now = Date.now();
    for (const containerName in errorLoopState) {
      const timestamps = errorLoopState[containerName];
      if (timestamps.length >= 5 && (now - timestamps[0]) < 10000) {
        newErrorMap[containerName] = true;
      }
    }
    return newErrorMap;
  }, [errorLoopState]);

  // エラー検出後は ERROR_HIGHLIGHT_MS の間だけ赤表示を保持し、その後リセットして再検出可能にする。
  // （検出直後に即リセットすると赤表示が一瞬で消えてしまうため、遅延させる）
  useEffect(() => {
    containers.forEach((c) => {
      if (errorLogsMap[c.name] && c.state === 'running' && !errorClearTimeoutsRef.current[c.name]) {
        errorClearTimeoutsRef.current[c.name] = setTimeout(() => {
          delete errorClearTimeoutsRef.current[c.name];
          setErrorLoopState((prev) => {
            const newState = { ...prev };
            delete newState[c.name];
            return newState;
          });
        }, ERROR_HIGHLIGHT_MS);
      }
    });
  }, [containers, errorLogsMap]);

  useEffect(() => {
    const timeouts = errorClearTimeoutsRef.current;
    const sockets = wsMapRef.current;
    return () => {
      Object.values(timeouts).forEach((t) => clearTimeout(t));
      Object.values(sockets).forEach((ws) => ws.close());
    };
  }, []);

  const statusOf = useCallback((c) => getStatus(c, !!errorLogsMap[c.name]), [errorLogsMap]);

  // --- ツリー: プロジェクト → グループ（機能 or レイヤー）→ コンテナ。自プロジェクト以外はグループ分けしない ---
  const tree = useMemo(() => {
    const q = query.trim().toLowerCase();
    const visible = containers.filter((c) =>
      !isSystemContainer(c.name)
      && (showCompleted || !isCompleted(c))
      && (!q || c.name.toLowerCase().includes(q) || (c.image || '').toLowerCase().includes(q)));

    const byProject = new Map();
    visible.forEach((c) => {
      const p = c.project || NO_PROJECT;
      if (!byProject.has(p)) byProject.set(p, []);
      byProject.get(p).push(c);
    });

    const mode = GROUP_MODES[groupBy];
    const projects = [...byProject.keys()].sort((a, b) => (a === ownProject ? -1 : b === ownProject ? 1 : a.localeCompare(b)));
    return projects.map((p) => {
      const members = byProject.get(p);
      if (p !== ownProject) {
        return { id: `p:${p}`, label: p, members: members.sort((a, b) => a.name.localeCompare(b.name)), groups: null };
      }
      const groups = new Map();
      members.forEach((c) => {
        const k = mode.keyOf(c);
        if (!groups.has(k)) groups.set(k, []);
        groups.get(k).push(c);
      });
      return {
        id: `p:${p}`,
        label: p,
        members,
        groups: [...groups.entries()]
          .sort((a, b) => mode.groupIndex[a[0]] - mode.groupIndex[b[0]])
          .map(([k, list]) => ({
            id: `g:${p}:${groupBy}:${k}`,
            label: mode.groupLabel(k),
            members: list.sort((a, b) => mode.childIndex(a) - mode.childIndex(b) || a.name.localeCompare(b.name)),
          })),
      };
    });
  }, [containers, ownProject, groupBy, query, showCompleted]);

  // 検索中は全ノードを開く。既定は自プロジェクトとそのグループが開、他プロジェクトが閉
  const searching = query.trim() !== '';
  const isOpen = (id, defaultOpen) => searching || (openMap[id] ?? defaultOpen);
  const toggle = (id, defaultOpen) => setOpenMap((prev) => ({ ...prev, [id]: !(prev[id] ?? defaultOpen) }));
  const summarize = (list) => ({
    status: worstStatus(list.map(statusOf)),
    running: list.filter((c) => c.state === 'running').length,
    total: list.length,
  });

  // --- 選択中のコンテナ ---
  const selected = useMemo(() => containers.find((c) => c.name === selectedName) || null, [containers, selectedName]);

  const networksByName = useMemo(() => Object.fromEntries(topology.map((n) => [n.name, n.networks || []])), [topology]);

  // 依存関係（compose の depends_on ラベル）。依存先 = selected が待つサービス、依存元 = selected を待つサービス
  const { deps, rdeps } = useMemo(() => {
    if (!selected?.project) return { deps: [], rdeps: [] };
    const sameProject = containers.filter((c) => c.project === selected.project && !isSystemContainer(c.name));
    const byService = Object.fromEntries(sameProject.map((c) => [c.service, c]));
    return {
      deps: (selected.dependsOn || []).map((s) => byService[s]).filter(Boolean),
      rdeps: sameProject.filter((c) => (c.dependsOn || []).includes(selected.service)),
    };
  }, [containers, selected]);

  const checkHasStorage = useCallback((container) => !!container && (
    hasStorageMap[container.name]
    || ['minio', 'storage'].some((kw) => (container.name || '').toLowerCase().includes(kw) || (container.image || '').toLowerCase().includes(kw))
  ), [hasStorageMap]);
  const selectedHasStorage = checkHasStorage(selected);

  const handleSelect = (name) => {
    setSelectedName(name);
    setActiveTab(checkHasStorage(containers.find((x) => x.name === name)) ? 'storage' : 'logs');
    setCurrentBucket('');
    setCurrentPrefix('');
  };

  // --- MinIO（Storage タブ） ---
  const fetchItems = useCallback(async () => {
    if (!selectedName) return;
    setLoading(true);
    setSelectedItem(null);
    try {
      const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix });
      const res = await fetch(`/api/minio/list/${selectedName}?${params.toString()}`);
      const data = await res.json();
      setItems(Array.isArray(data) ? data : []);
    } catch (err) {
      console.error(err);
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [selectedName, currentBucket, currentPrefix]);

  useEffect(() => {
    if (selectedHasStorage && activeTab === 'storage') fetchItems();
  }, [selectedHasStorage, activeTab, fetchItems]);

  const handleDoubleClick = (item) => {
    if (item.type === 'bucket') { setCurrentBucket(item.name); setCurrentPrefix(''); }
    else if (item.type === 'folder') setCurrentPrefix((prev) => `${prev}${item.name}/`);
  };
  const handleGoBack = () => {
    if (currentPrefix) {
      const parts = currentPrefix.split('/').filter(Boolean);
      parts.pop();
      setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : '');
    } else if (currentBucket) setCurrentBucket('');
  };
  const handleUpload = async (e) => {
    const file = e.target.files[0];
    if (!file || !currentBucket) return;
    const formData = new FormData();
    formData.append('file', file);
    const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix });
    const res = await fetch(`/api/minio/upload/${selectedName}?${params.toString()}`, { method: 'POST', body: formData });
    if (res.ok) fetchItems(); else alert('Upload failed');
  };
  const handleDelete = async () => {
    if (!selectedItem || selectedItem.type !== 'file') return;
    if (!confirm(`Delete "${selectedItem.name}"?`)) return;
    const params = new URLSearchParams({ bucket: currentBucket, key: currentPrefix + selectedItem.name });
    const res = await fetch(`/api/minio/delete/${selectedName}?${params.toString()}`, { method: 'DELETE' });
    if (res.ok) fetchItems(); else alert('Delete failed');
  };
  const storageProps = { loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete };

  // --- 概要（自プロジェクトのみ集計） ---
  const ownContainers = useMemo(() => containers.filter((c) => c.project === ownProject && !isSystemContainer(c.name)), [containers, ownProject]);

  const { counts, issues } = useMemo(() => {
    const cnt = { error: 0, down: 0, warn: 0, ok: 0, done: 0 };
    const list = [];
    ownContainers.forEach((c) => {
      const s = statusOf(c);
      cnt[s] += 1;
      const reasons = [];
      if (errorLogsMap[c.name]) reasons.push('エラーログが 10 秒間に 5 件以上');
      if (c.health === 'unhealthy') reasons.push('ヘルスチェック失敗（unhealthy）');
      if (s === 'down') reasons.push(c.status);
      if (s === 'warn') reasons.push(c.health === 'starting' ? 'ヘルスチェック待ち' : c.status);
      if (c.restartCount > 0) reasons.push(`再起動 ${c.restartCount} 回`);
      if (reasons.length > 0) list.push({ c, reasons });
    });
    list.sort((a, b) => SEVERITY[statusOf(b.c)] - SEVERITY[statusOf(a.c)] || a.c.name.localeCompare(b.c.name));
    return { counts: cnt, issues: list };
  }, [ownContainers, statusOf, errorLogsMap]);

  // CPU は docker stats と同じく 1 コア = 100%。メモリの分母は、制限なしのコンテナが報告するホストのメモリ量（limit の最大値）
  const load = useMemo(() => {
    let cpu = 0;
    let mem = 0;
    let host = 0;
    ownContainers.forEach((c) => {
      const s = containerStats[c.name];
      if (!s) return;
      cpu += s.cpuPercent || 0;
      mem += s.memUsage || 0;
      host = Math.max(host, s.memLimit || 0);
    });
    return { cpu, mem, host };
  }, [ownContainers, containerStats]);

  return (
    <div className="h-screen flex flex-col bg-slate-950 text-slate-200">
      <header className="flex items-center gap-3 px-4 h-14 border-b border-slate-800 shrink-0">
        <Boxes className="text-cyan-400" size={20} />
        <h1 className="font-semibold tracking-wide text-slate-100">Atomosidea Monitor</h1>
        <div className="ml-auto flex items-center gap-2">
          <Metric icon={Activity} label="LPS" value={lps.toFixed(1)} title="1 秒あたりのログ行数（全コンテナ合計）"><Sparkline values={lpsHistory} /></Metric>
          <Metric icon={Users} label="Users" value={activeUsers} sub="5分" title="atmosidea-frontend のログに出た IP の数（推定）" />
          <Metric icon={Cpu} label="CPU" value={`${load.cpu.toFixed(0)}%`} title="自プロジェクトのコンテナ合計（1 コア = 100%）" />
          <Metric icon={HardDrive} label="MEM" value={formatBytes(load.mem)} sub={load.host ? `/ ${formatBytes(load.host)}` : ''} title="自プロジェクトのコンテナ合計 / ホストのメモリ" />
          <button onClick={() => setShowVolumes(true)} className="text-xs px-3 py-2 rounded-lg bg-slate-900/70 border border-slate-800 text-slate-300 hover:border-cyan-500/50 hover:text-cyan-300 flex items-center gap-1.5">
            <HardDrive size={13} />Volumes
          </button>
        </div>
      </header>

      <div className="flex-1 flex min-h-0">
        <aside className="w-[440px] shrink-0 border-r border-slate-800 flex flex-col min-h-0">
          <div className="p-3 space-y-2 border-b border-slate-800 shrink-0">
            <div className="flex bg-slate-900 p-0.5 rounded-lg border border-slate-800 text-xs">
              {Object.entries(GROUP_MODES).map(([key, m]) => (
                <button key={key} onClick={() => setGroupBy(key)} className={`flex-1 px-3 py-1.5 rounded-md ${groupBy === key ? 'bg-cyan-600 text-white' : 'text-slate-400 hover:text-slate-200'}`}>
                  {m.label}
                </button>
              ))}
            </div>
            <div className="flex items-center gap-2">
              <div className="flex-1 flex items-center gap-2 px-2.5 py-1.5 rounded-lg bg-slate-900 border border-slate-800 focus-within:border-cyan-500/50">
                <Search size={14} className="text-slate-500 shrink-0" />
                <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="名前・イメージで絞り込み" className="flex-1 bg-transparent outline-none text-xs text-slate-200 placeholder:text-slate-600" />
                {query && <button onClick={() => setQuery('')} className="text-slate-500 hover:text-slate-300"><X size={13} /></button>}
              </div>
              <label className="flex items-center gap-1.5 text-[11px] text-slate-400 cursor-pointer select-none whitespace-nowrap" title="exit 0 で終わった init 系のコンテナを表示する">
                <input type="checkbox" checked={showCompleted} onChange={(e) => setShowCompleted(e.target.checked)} className="accent-cyan-500" />
                正常終了も表示
              </label>
            </div>
          </div>

          <div className="flex-1 overflow-y-auto py-1">
            {tree.length === 0 && <div className="p-4 text-xs text-slate-600">該当するコンテナがありません。</div>}
            {tree.map((p) => {
              const own = p.label === ownProject;
              const pOpen = isOpen(p.id, own);
              const renderRow = (c, depth) => (
                <ContainerRow
                  key={c.name}
                  c={c}
                  depth={depth}
                  status={statusOf(c)}
                  badge={own ? GROUP_MODES[groupBy].badgeOf(c) : null}
                  stats={containerStats[c.name]}
                  isActive={!!activeLogsMap[c.name]}
                  isSelected={c.name === selectedName}
                  onSelect={handleSelect}
                />
              );
              return (
                <div key={p.id} className={own ? '' : 'mt-1'}>
                  <GroupRow label={p.label} depth={0} open={pOpen} onToggle={() => toggle(p.id, own)} strong {...summarize(p.members)} />
                  {pOpen && (p.groups
                    ? p.groups.map((g) => {
                      const gOpen = isOpen(g.id, true);
                      return (
                        <div key={g.id}>
                          <GroupRow label={g.label} depth={1} open={gOpen} onToggle={() => toggle(g.id, true)} {...summarize(g.members)} />
                          {gOpen && g.members.map((c) => renderRow(c, 2))}
                        </div>
                      );
                    })
                    : p.members.map((c) => renderRow(c, 1)))}
                </div>
              );
            })}
          </div>
        </aside>

        <main className="flex-1 min-w-0 flex flex-col min-h-0">
          {selected ? (
            <DetailPane
              c={selected}
              status={statusOf(selected)}
              stats={containerStats[selected.name]}
              networks={networksByName[selected.name] || []}
              deps={deps}
              rdeps={rdeps}
              statusOf={statusOf}
              onSelect={handleSelect}
              onClose={() => setSelectedName(null)}
              activeTab={activeTab}
              setActiveTab={setActiveTab}
              canShowStorage={selectedHasStorage}
              logs={logsMap[selected.name] || []}
              storageProps={storageProps}
            />
          ) : (
            <Overview project={ownProject} counts={counts} issues={issues} statusOf={statusOf} onSelect={handleSelect} />
          )}
        </main>
      </div>

      {showVolumes && <VolumesModal onClose={() => setShowVolumes(false)} volumes={volumes} />}
    </div>
  );
}
