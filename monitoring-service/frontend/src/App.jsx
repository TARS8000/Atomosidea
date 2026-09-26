import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import {
  Folder, FileText, Database, ChevronRight, ArrowLeft, Server, Upload, Trash2, RefreshCw, Terminal, HardDrive,
  RotateCw, Loader2, AlertTriangle, X, Cpu, GitBranch, Waypoints, Activity, Users
} from 'lucide-react';

// --- SFチックなカスタムCSS ---
const CyberStyles = () => (
  <style>{`
    .bg-cyber-grid {
      background-image: 
        linear-gradient(to right, rgba(59, 130, 246, 0.08) 1px, transparent 1px),
        linear-gradient(to bottom, rgba(59, 130, 246, 0.08) 1px, transparent 1px);
      background-size: 32px 32px;
    }

    @keyframes flowPacket {
      0% { stroke-dashoffset: 24; }
      100% { stroke-dashoffset: 0; }
    }

    .path-packet-flow {
      stroke-dasharray: 6, 18;
      animation: flowPacket 0.6s linear infinite;
    }

    .cyber-card {
      clip-path: polygon(0 0, calc(100% - 12px) 0, 100% 12px, 100% 100%, 12px 100%, 0 calc(100% - 12px));
    }
    
    .animate-spin-slow {
      animation: spin 8s linear infinite;
    }
  `}</style>
);

// --- Component: AccessMonitor ---
const AccessMonitor = ({ lps, history }) => {
  const maxLps = Math.max(1, ...history);
  const points = history.map((val, i) => `${(i / (history.length - 1)) * 100},${100 - (val / maxLps) * 100}`).join(' ');

  return (
    <div className="bg-slate-950/80 backdrop-blur-md border border-cyan-500/30 rounded-lg p-3 w-64 cyber-card">
      <div className="flex items-center justify-between text-cyan-400">
        <h2 className="font-mono text-sm font-bold tracking-wider flex items-center gap-2"><Activity size={16} />SYSTEM ACTIVITY</h2>
      </div>
      <div className="mt-2 flex items-end gap-2">
        <div className="font-mono text-3xl font-bold text-white leading-none">{lps.toFixed(1)}</div>
        <div className="text-xs text-slate-400 font-mono -mb-0.5">LPS</div>
        <svg width="120" height="30" className="flex-1 -mb-1">
          <polyline points={points} fill="none" stroke="rgba(6, 182, 212, 0.7)" strokeWidth="2" />
        </svg>
      </div>
    </div>
  );
};

// --- Component: UserCountMonitor ---
const UserCountMonitor = ({ count }) => (
  <div className="bg-slate-950/80 backdrop-blur-md border border-cyan-500/30 rounded-lg p-3 w-64 cyber-card">
    <div className="flex items-center justify-between text-cyan-400">
      <h2 className="font-mono text-sm font-bold tracking-wider flex items-center gap-2"><Users size={16} />ACTIVE USERS</h2>
    </div>
    <div className="mt-2 flex items-end gap-2">
      <div className="font-mono text-3xl font-bold text-white leading-none">{count}</div>
      <div className="text-xs text-slate-400 font-mono -mb-0.5">Users (5min)</div>
    </div>
  </div>
);

// --- Component: SystemLoadMonitor ---
const SystemLoadMonitor = ({ totalCpu, totalMemUsageGB, totalMemLimitGB }) => {
  const getBarColor = (percentage) => {
    if (percentage > 90) return 'bg-rose-500';
    if (percentage > 70) return 'bg-amber-500';
    return 'bg-emerald-500';
  };

  const memPercentage = totalMemLimitGB > 0 ? (totalMemUsageGB / totalMemLimitGB) * 100 : 0;

  return (
    <div className="bg-slate-950/80 backdrop-blur-md border border-cyan-500/30 rounded-lg p-3 w-64 cyber-card">
      <div className="flex items-center justify-between text-cyan-400">
        <h2 className="font-mono text-sm font-bold tracking-wider flex items-center gap-2"><Cpu size={16} />SYSTEM LOAD</h2>
      </div>
      <div className="mt-2 space-y-1.5">
        <div className="flex items-center gap-1.5">
          <span className="text-[9px] font-mono text-slate-500 w-8">CPU</span>
          <div className="w-full bg-slate-700/50 h-1.5 rounded-full overflow-hidden">
            <div className={`h-full rounded-full ${getBarColor(totalCpu)}`} style={{ width: `${totalCpu}%` }} />
          </div>
          <span className="text-[9px] font-mono text-slate-400 w-8 text-right">{totalCpu.toFixed(0)}%</span>
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[9px] font-mono text-slate-500 w-8">MEM</span>
          <div className="w-full bg-slate-700/50 h-1.5 rounded-full overflow-hidden">
            <div className={`h-full rounded-full ${getBarColor(memPercentage)}`} style={{ width: `${memPercentage}%` }} />
          </div>
          <span className="text-[9px] font-mono text-slate-400 w-8 text-right">{totalMemUsageGB.toFixed(1)}GB</span>
        </div>
      </div>
    </div>
  );
};


// --- Component: ConnectionLine (縦方向 上→下 ルーティング) ---
const ConnectionLine = ({ from, to, isActive, hasError, networks }) => {
  if (!from || !to) return null;

  // 上から下への配線：送信元の「下端中央」 -> 送信先の「上端中央」
  const startX = from.x + 96;  // ノード幅 192px の中央
  const startY = from.y + 96;  // ノード高さ 96px の下端
  const endX = to.x + 96;
  const endY = to.y;           // ノードの上端

  // 縦方向の直角（Orthogonal）配線パス
  const midY = startY + (endY - startY) / 2;
  const d = `M ${startX} ${startY} L ${startX} ${midY} L ${endX} ${midY} L ${endX} ${endY}`;

  const glowColor = hasError ? '#ef4444' : networkColor(networks);

  return (
    <g className="pointer-events-none">
      <path
        d={d}
        stroke={isActive ? '#0284c7' : '#1e293b'}
        strokeWidth="1.5"
        fill="none"
        opacity={isActive ? "0.8" : "0.3"}
      />

      {isActive && (
        <>
          <path
            d={d}
            stroke={glowColor}
            strokeWidth="4"
            fill="none"
            opacity="0.3"
            className="blur-[2px]"
          />
          <path
            d={d}
            stroke={glowColor}
            strokeWidth="2.5"
            fill="none"
            className="path-packet-flow"
            style={{ filter: `drop-shadow(0 0 5px ${glowColor})` }}
          />
        </>
      )}

      <circle cx={startX} cy={startY} r="2.5" fill={isActive ? glowColor : '#334155'} />
      <circle cx={endX} cy={endY} r="2.5" fill={isActive ? glowColor : '#334155'} />
    </g>
  );
};

// --- Component: ContainerNode ---
const ContainerNode = ({ container, onClick, isActive, hasError, position, stats, health, restartCount, uptime }) => {
  const isRunning = container.state === 'running';
  const isStarting = container.state === 'starting' || container.state === 'restarting';
  const isUnhealthy = health === 'unhealthy';
  const isHealthy = health === 'healthy';

  const themeBorder = hasError || isUnhealthy
    ? 'border-rose-500/80 shadow-rose-900/40'
    : isActive
    ? 'border-cyan-400/80 shadow-cyan-500/30'
    : isRunning
    ? 'border-slate-800 hover:border-slate-600'
    : 'border-slate-900 opacity-50';

  const cpuUsage = stats?.cpuPercent || 0;
  const memUsage = stats ? (stats.memUsage / stats.memLimit) * 100 : 0;

  const getBarColor = (percentage) => {
    if (percentage > 90) return 'bg-rose-500';
    if (percentage > 70) return 'bg-amber-500';
    return 'bg-emerald-500';
  };

  return (
    <div
      id={`node-${container.name}`}
      onClick={() => onClick(container)}
      className="absolute transition-all duration-300 cursor-pointer group z-10"
      style={{ top: `${position.y}px`, left: `${position.x}px` }}
    >
      {isActive && (
        <div className="absolute -inset-1 bg-cyan-500/20 rounded-lg blur-sm animate-pulse" />
      )}

      <div className={`w-44 h-24 p-2 bg-slate-950/85 backdrop-blur-md border ${themeBorder} cyber-card hover:border-cyan-400 transition-all shadow-xl relative overflow-hidden flex flex-col justify-between`}>
        <div className="absolute inset-0 bg-[linear-gradient(rgba(18,16,16,0)_50%,rgba(0,0,0,0.3)_50%)] bg-[length:100%_4px] pointer-events-none opacity-40" />

        <div className="flex justify-between items-start relative z-10">
          <div className="text-xs font-mono font-bold text-slate-200 group-hover:text-cyan-300 truncate tracking-wide">
            {container.name}
          </div>
          <div className="flex items-center gap-1">
            {isStarting && <Loader2 className="w-3 h-3 text-amber-400 animate-spin" />}
            {(isHealthy || isUnhealthy) && (
              <span className={`w-2 h-2 rounded-full ${isUnhealthy ? 'bg-rose-500 animate-pulse' : 'bg-emerald-500'}`} title={`health: ${health}`} />
            )}
            {!isHealthy && !isUnhealthy && isRunning && (
              <span className={`w-2 h-2 rounded-full ${isActive ? 'bg-cyan-400 shadow-[0_0_8px_#22d3ee]' : 'bg-slate-600'}`} />
            )}
          </div>
        </div>

        <div className="text-[10px] font-mono text-slate-500 truncate relative z-10 -mt-1">
          {container.image}
        </div>

        <div className="flex items-center justify-between z-10 -mt-0.5 text-[8px] font-mono text-slate-500">
          {restartCount > 0 ? <span>↻ {restartCount} restarts</span> : <span />}
          <span>{uptime ? `⏱ ${uptime}` : ''}</span>
        </div>

        <div className="space-y-1.5 relative z-10">
          <div className="flex items-center gap-1.5">
            <span className="text-[9px] font-mono text-slate-500">CPU</span>
            <div className="w-full bg-slate-700/50 h-1.5 rounded-full overflow-hidden">
              <div className={`h-full rounded-full ${getBarColor(cpuUsage)}`} style={{ width: `${cpuUsage}%` }} />
            </div>
            <span className="text-[9px] font-mono text-slate-400 w-8 text-right">{cpuUsage.toFixed(0)}%</span>
          </div>
          <div className="flex items-center gap-1.5">
            <span className="text-[9px] font-mono text-slate-500">MEM</span>
            <div className="w-full bg-slate-700/50 h-1.5 rounded-full overflow-hidden">
              <div className={`h-full rounded-full ${getBarColor(memUsage)}`} style={{ width: `${memUsage}%` }} />
            </div>
            <span className="text-[9px] font-mono text-slate-400 w-8 text-right">{memUsage.toFixed(0)}%</span>
          </div>
        </div>

        <div className="flex items-center justify-between z-10 -mt-1">
          <div className="h-[2px] w-6 bg-slate-800 group-hover:bg-cyan-500/50 transition-colors" />
          <span className={`text-[9px] font-mono px-1 py-0.5 uppercase tracking-wider ${
            hasError ? 'bg-rose-950/80 text-rose-400 border border-rose-800/50' :
            isRunning ? 'text-cyan-400/90' : 'text-slate-600'
          }`}>
            [{container.state}]
          </span>
        </div>
      </div>
    </div>
  );
};

// --- Storage & Log Modals (略: 変更なし) ---
const StorageExplorer = ({ loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete }) => (
  <div className="flex-1 bg-slate-950/50 border border-slate-800 rounded-xl flex flex-col overflow-hidden h-full">
    <div className="p-2 border-b border-slate-800 bg-slate-900/50 flex items-center justify-between gap-2">
      <div className="flex items-center gap-2">
        <button onClick={handleGoBack} disabled={!currentBucket && !currentPrefix} className="p-1.5 hover:bg-slate-800 rounded disabled:opacity-30 text-slate-300"><ArrowLeft className="w-4 h-4" /></button>
        <div className="flex items-center gap-1 bg-slate-900 border border-slate-800 px-3 py-1 rounded text-xs text-slate-300 font-mono">
          <span className="cursor-pointer hover:underline text-blue-400">Root</span>
          {currentBucket && <><ChevronRight className="w-3 h-3 text-slate-600" /><span className="cursor-pointer hover:underline text-blue-400">{currentBucket}</span></>}
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

const LogViewerModal = ({ container, onClose, logs, logEndRef, activeTab, setActiveTab, canShowStorage, storageProps }) => {
  if (!container) return null;
  return (
    <div className="absolute inset-0 bg-black/60 backdrop-blur-md z-30 flex items-center justify-center p-4">
      <div className="w-full h-full max-w-6xl max-h-[90vh] bg-slate-950/90 border border-cyan-500/30 rounded-2xl shadow-2xl flex flex-col">
        <div className="p-4 border-b border-slate-800 flex items-center justify-between">
          <h1 className="font-mono font-bold text-lg text-slate-100 flex items-center gap-2"><Cpu className="w-5 h-5 text-cyan-400" />{container.name}</h1>
          <div className="flex items-center gap-2">
            <div className="flex bg-slate-900 p-1 rounded-lg border border-slate-800 text-xs font-mono">
              {canShowStorage && (
                <button onClick={() => setActiveTab('storage')} className={`px-3 py-1.5 rounded-md ${activeTab === 'storage' ? 'bg-cyan-600 text-white' : 'text-slate-400'}`}>Storage</button>
              )}
              <button onClick={() => setActiveTab('logs')} className={`px-3 py-1.5 rounded-md ${activeTab === 'logs' || !canShowStorage ? 'bg-cyan-600 text-white' : 'text-slate-400'}`}>Logs</button>
            </div>
            <button onClick={onClose} className="p-2 text-slate-400 hover:text-white"><X className="w-5 h-5" /></button>
          </div>
        </div>
        <div className="flex-1 p-4 overflow-hidden">
          {activeTab === 'storage' && canShowStorage ? <StorageExplorer {...storageProps} /> : (
            <div className="flex-1 bg-black/60 border border-slate-800 rounded-xl p-4 overflow-y-auto font-mono text-xs space-y-1 h-full select-text">
              {logs.length === 0 ? <div className="text-slate-600 italic select-none">Streaming logs...</div> : logs.map((log, index) => <div key={index} className={`whitespace-pre-wrap break-all ${/(error|fatal|fail|exception|stderr|panic)/i.test(log) ? 'text-rose-400' : 'text-slate-300'}`}>{log}</div>)}
              <div ref={logEndRef} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

// --- Monitoring topology configuration ---
const MONITOR_LAYERS = {
  'UI': { row: 0, title: '1. Frontend Layer' },
  'API': { row: 1, title: '2. API Gateway & Services (Backend)' },
  'Workers': { row: 2, title: '3. Security Scan & Processing Workers (Backend)' },
  'DB': { row: 3, title: '4. Databases & Cache' },
  'Storage': { row: 4, title: '5. Storage & Object Persistence' },
  'Unknown': { row: 5, title: 'Uncategorized' },
};

const getContainerGroup = (name) => {
  const n = name.toLowerCase();
  if (n.endsWith('frontend') || n.includes('frontend-builder')) return 'UI';
  if (
    n.endsWith('auth-service') ||
    n.endsWith('profile-service') ||
    n.endsWith('team-service') ||
    n.endsWith('mypage-service')
  ) return 'API';
  if (
    n.endsWith('sfsp-api') ||
    n.endsWith('sfsp-worker') ||
    n.endsWith('video-worker') ||
    n.endsWith('game-worker') ||
    n.endsWith('static-site-worker') ||
    n.endsWith('upload-api') ||
    n.includes('clamav') ||
    n.includes('yara')
  ) return 'Workers';
  if (
    n.endsWith('auth-db') ||
    n.endsWith('app-db') ||
    n.endsWith('profile-db') ||
    n.endsWith('team-db') ||
    n.endsWith('sfsp-db') ||
    n.endsWith('redis')
  ) return 'DB';
  if (
    n.endsWith('profile-storage') ||
    n.endsWith('game-storage') ||
    n.endsWith('static-site-storage') ||
    n.endsWith('video-storage') ||
    n.includes('minio') ||
    n.includes('storage')
  ) return 'Storage';
  return 'Unknown';
};

const groupLabelMap = { UI: 'Frontend', Backend: 'Backend', DB: 'DB/Cache', Storage: 'Storage' };

const SRC_LAYERS = new Set(['UI', 'API']);
const DEP_LAYERS = new Set(['Workers', 'DB', 'Storage', 'Unknown']);
const SYSTEM_CONTAINERS = new Set(['mon-backend', 'mon-nginx', 'mon-frontend-builder']);

// プロジェクト所属外のコンテナ（監視対象外）
const EXTERNAL_CONTAINERS = new Set(['ba_halo_db', 'ba_halo_worker', 'open-webui']);

const isSystemContainer = (name) => SYSTEM_CONTAINERS.has(name) || (name || '').startsWith('mon-') || /-builder$/.test(name) || EXTERNAL_CONTAINERS.has(name);

// 同一サービス群の共通接頭辞抽出（game-upload-api/game-worker -> "game"、sfsp-api/sfsp-worker/sfsp-db -> "sfsp"）
const getBaseName = (name) => {
  let n = name.replace(/^atmosidea-/i, '');
  n = n.replace(/-(service|db|worker|storage|upload-api|api|builder)$/i, '');
  return n || name;
};

const networkColor = (nets) => {
  if (!nets || nets.length === 0) return '#06b6d4';
  if (nets.some((n) => n.includes('sfsp'))) return '#f59e0b';
  if (nets.some((n) => n.includes('db'))) return '#10b981';
  return '#3b82f6';
};

// --- Monitoring Volumes Panel ---
const VolumesModal = ({ onClose, volumes }) => (
  <div className="absolute inset-0 bg-black/60 backdrop-blur-md z-30 flex items-center justify-center p-4">
    <div className="w-full max-w-2xl bg-slate-950/90 border border-cyan-500/30 rounded-2xl shadow-2xl flex flex-col">
      <div className="p-4 border-b border-slate-800 flex items-center justify-between">
        <h1 className="font-mono font-bold text-lg text-slate-100 flex items-center gap-2"><HardDrive className="w-5 h-5 text-cyan-400" />Persistent Volumes</h1>
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
  const [selectedContainer, setSelectedContainer] = useState(null);
  const [activeTab, setActiveTab] = useState('logs');
  const [logsMap, setLogsMap] = useState({});
  const [activeLogsMap, setActiveLogsMap] = useState({});
  const [errorLoopState, setErrorLoopState] = useState({});
  const [restartingContainers, setRestartingContainers] = useState(new Set());
  const [hasStorageMap, setHasStorageMap] = useState({});
  const [items, setItems] = useState([]);
  const [selectedItem, setSelectedItem] = useState(null);
  const [loading, setLoading] = useState(false);
  const [currentBucket, setCurrentBucket] = useState('');
  const [currentPrefix, setCurrentPrefix] = useState('');
  const [lps, setLps] = useState(0);
  const [lpsHistory, setLpsHistory] = useState(new Array(30).fill(0));
  const [activeUsers, setActiveUsers] = useState(0);
  const [topology, setTopology] = useState([]);
  const [volumes, setVolumes] = useState([]);
  const [showVolumes, setShowVolumes] = useState(false);
  const logCountRef = useRef(0);

  const logEndRef = useRef(null);
  const wsMapRef = useRef({});
  const pulseTimeoutsRef = useRef({});
  const reconnectTimeoutsRef = useRef({});
  const fileInputRef = useRef(null);

  const fetchContainers = useCallback(async () => {
    try {
      const res = await fetch('/api/containers');
      const data = await res.json();
      setContainers(data);
    } catch (err) {
      console.error('Fetch containers error:', err);
    }
  }, []);

  const fetchContainerStats = useCallback(async () => {
    try {
      const res = await fetch('/api/containers/stats');
      const data = await res.json();
      setContainerStats(data);
    } catch (err) {
      console.error('Fetch container stats error:', err);
    }
  }, []);

  const fetchActiveUsers = useCallback(async () => {
    try {
      const res = await fetch('/api/connections/count');
      const data = await res.json();
      setActiveUsers(data.count);
    } catch (err) {
      console.error('Fetch active users error:', err);
    }
  }, []);

  const fetchTopology = useCallback(async () => {
    try {
      const res = await fetch('/api/topology');
      const data = await res.json();
      setTopology(Array.isArray(data) ? data : []);
    } catch (err) {
      console.error('Fetch topology error:', err);
    }
  }, []);

  const fetchVolumes = useCallback(async () => {
    try {
      const res = await fetch('/api/volumes');
      const data = await res.json();
      setVolumes(Array.isArray(data) ? data : []);
    } catch (err) {
      console.error('Fetch volumes error:', err);
    }
  }, []);

  useEffect(() => {
    fetchContainers();
    fetchActiveUsers();
    fetchContainerStats();
    fetchTopology();
    fetchVolumes();
    const containerInterval = setInterval(fetchContainers, 3000);
    const statsInterval = setInterval(fetchContainerStats, 3000);
    const userInterval = setInterval(fetchActiveUsers, 10000);
    return () => {
      clearInterval(containerInterval);
      clearInterval(statsInterval);
      clearInterval(userInterval);
    };
  }, [fetchContainers, fetchActiveUsers, fetchContainerStats, fetchTopology, fetchVolumes]);

  useEffect(() => {
    const lpsInterval = setInterval(() => {
      setLps(logCountRef.current / 2);
      setLpsHistory(prev => [...prev.slice(1), logCountRef.current / 2]);
      logCountRef.current = 0;
    }, 2000);
    return () => clearInterval(lpsInterval);
  }, []);

  useEffect(() => {
    if (containers.length === 0) return;
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    containers.forEach(c => {
      if (isSystemContainer(c.name)) return;
      if (!(c.state === 'running' || c.state === 'starting' || c.state === 'restarting')) {
        if (wsMapRef.current[c.name]) { wsMapRef.current[c.name].close(); delete wsMapRef.current[c.name]; }
        return;
      }
      if (wsMapRef.current[c.name] && (wsMapRef.current[c.name].readyState === WebSocket.OPEN || wsMapRef.current[c.name].readyState === WebSocket.CONNECTING)) return;
      
      const socket = new WebSocket(`${protocol}//${window.location.host}/ws/logs?container=${c.name}`);
      socket.onopen = () => { if (reconnectTimeoutsRef.current[c.name]) clearTimeout(reconnectTimeoutsRef.current[c.name]); };
      socket.onmessage = (event) => {
        logCountRef.current += 1;
        const logData = event.data;
        if (logData.includes('min.io')) setHasStorageMap(prev => ({ ...prev, [c.name]: true }));
        
        if (/(error|fatal|fail|exception|stderr|panic)/i.test(logData)) {
          setErrorLoopState(prev => {
            const now = Date.now();
            const newTimestamps = [...(prev[c.name] || []), now].slice(-5);
            return { ...prev, [c.name]: newTimestamps };
          });
        }

        setLogsMap(prev => ({ ...prev, [c.name]: [...(prev[c.name] || []).slice(-200), logData] }));
        setActiveLogsMap(prev => ({ ...prev, [c.name]: true }));
        
        if (pulseTimeoutsRef.current[c.name]) clearTimeout(pulseTimeoutsRef.current[c.name]);
        pulseTimeoutsRef.current[c.name] = setTimeout(() => setActiveLogsMap(prev => ({ ...prev, [c.name]: false })), 600);
      };
      socket.onclose = () => { delete wsMapRef.current[c.name]; reconnectTimeoutsRef.current[c.name] = setTimeout(() => socket.onopen(), 3000); };
      socket.onerror = (err) => { console.error(`WebSocket Error [${c.name}]:`, err); socket.close(); };
      wsMapRef.current[c.name] = socket;
    });

    Object.keys(wsMapRef.current).forEach(name => {
      if (!containers.some(c => c.name === name && (c.state === 'running' || c.state === 'starting' || c.state === 'restarting'))) {
        if (reconnectTimeoutsRef.current[name]) clearTimeout(reconnectTimeoutsRef.current[name]);
        wsMapRef.current[name]?.close();
        delete wsMapRef.current[name];
      }
    });
  }, [containers]);

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

  useEffect(() => {
    containers.forEach(c => {
      if (errorLogsMap[c.name] && c.state === 'running' && !restartingContainers.has(c.name)) {
        setErrorLoopState(prev => {
          const newState = { ...prev };
          delete newState[c.name];
          return newState;
        });
      }
    });
  }, [containers, errorLogsMap, restartingContainers]);

  useEffect(() => () => {
    Object.values(wsMapRef.current).forEach(ws => ws.close());
    Object.values(pulseTimeoutsRef.current).forEach(t => clearTimeout(t));
    Object.values(reconnectTimeoutsRef.current).forEach(t => clearTimeout(t));
  }, []);

  const handleRestartContainer = async (containerName) => {
    if (!containerName || restartingContainers.has(containerName)) return;
    if (!confirm(`コンテナ「${containerName}」を再起動しますか？`)) return;

    setRestartingContainers(prev => new Set(prev).add(containerName));
    setErrorLoopState(prev => {
      const newState = { ...prev };
      delete newState[containerName];
      return newState;
    });

    try {
      const res = await fetch(`/api/containers/restart/${containerName}`, { method: 'POST' });
      if (!res.ok) alert('再起動に失敗しました');
      await fetchContainers();
    } catch (err) {
      console.error(err);
      alert('エラーが発生しました');
    } finally {
      setRestartingContainers(prev => {
        const newSet = new Set(prev);
        newSet.delete(containerName);
        return newSet;
      });
    }
  };

  // --- グリッド配置：横軸=カテゴリ（game等）、縦軸=サービス種別（Frontend/Backend/Workers/DB/Storage）---
  // 同種（game/video/sfsp等、接頭辞が同じ）のコンテナを1列（縦列）として、層順（API→Workers→DB→Storage）で縦積み。
  // 行（横列）はサービス種別でくくり、各カテゴリ列で縦積みを揃えて整列させる。
  const { containerPositions, layersLayout, canvasSize } = useMemo(() => {
    const cardW = 176; // w-44
    const cardH = 96; // h-24
    const gap = 12;
    const colGap = 40;
    const leftMargin = 96;
    const topMargin = 84;
    const rowLabelWidth = 160; // 縦軸ラベル用
    const positions = {};

    // 利用可能幅（右HUDパネル＋余白を除外）
    const availW = Math.max(900, (typeof window !== 'undefined' ? window.innerWidth : 1600) - 280);

    // 縦軸：サービス種別（層順）。Frontend最上段、Backend（API+Workers）その下。
    const rowOrder = ['UI', 'Backend', 'DB', 'Storage'];
    // コンテナグループをグリッド行にマッピング（APIとWorkersを統合）
    const rowMap = { UI: 'UI', API: 'Backend', Workers: 'Backend', DB: 'DB', Storage: 'Storage', Unknown: 'Storage' };

    // 横軸：カテゴリ（接頭辞）を抽出して昇順で固定
    const categories = new Set();
    containers.forEach(c => {
      if (isSystemContainer(c.name)) return;
      categories.add(getBaseName(c.name));
    });
    const catList = [...categories].sort((a, b) => a.localeCompare(b));

    // 各セル（カテゴリ×サービス種別）にコンテナを配置
    const grid = {};
    catList.forEach(cat => {
      grid[cat] = {};
      rowOrder.forEach(row => grid[cat][row] = []);
    });
    containers.forEach(c => {
      if (isSystemContainer(c.name)) return;
      const cat = getBaseName(c.name);
      const row = rowMap[getContainerGroup(c.name)] ?? 'Storage';
      if (!grid[cat][row]) grid[cat][row] = [];
      grid[cat][row].push(c);
    });

    // 各カテゴリ列の最大高を計算（層順に積み）
    const catHeights = {};
    catList.forEach(cat => {
      let maxH = 0;
      rowOrder.forEach(row => {
        const members = grid[cat][row];
        maxH = Math.max(maxH, members.length * (cardH + gap) - gap);
      });
      catHeights[cat] = maxH;
    });

    // 列を幅で貪欲パック（1画面に収める）
    const bands = [];
    let cur = [], curW = 0;
    catList.forEach(cat => {
      const w = cardW;
      if (cur.length && curW + colGap + w > availW) { bands.push(cur); cur = []; curW = 0; }
      cur.push(cat);
      curW += (cur.length > 1 ? colGap : 0) + w;
    });
    if (cur.length) bands.push(cur);

    // グリッド配置
    let cursorY = topMargin;
    let canvasWidth = leftMargin + rowLabelWidth + cardW;
    const gridMeta = [];

    bands.forEach(bandList => {
      // 各カテゴリ列の最大高で縦位置を揃える
      let bandHeight = 0;
      bandList.forEach(cat => {
        bandHeight = Math.max(bandHeight, catHeights[cat]);
      });

      bandList.forEach((cat, k) => {
        const x = leftMargin + rowLabelWidth + k * (cardW + colGap);
        // 縦列を層順に積み（各セルを配置）
        let rowY = cursorY;
        rowOrder.forEach(row => {
          const members = grid[cat][row];
          members.forEach((c, idx) => {
            positions[c.name] = { x, y: rowY + idx * (cardH + gap) };
          });
          rowY += members.length * (cardH + gap);
        });
        gridMeta.push({ cat, x });
      });

      canvasWidth = Math.max(canvasWidth, leftMargin + rowLabelWidth + bandList.length * (cardW + colGap) - colGap + 28);
      cursorY += bandHeight + 48; // 次バンド用のスペース
    });
    const canvasHeight = cursorY + 28;

    return {
      containerPositions: positions,
      layersLayout: { gridMeta, rowOrder, cardH, gap },
      canvasSize: { width: canvasWidth, height: canvasHeight }
    };
  }, [containers]);

  // --- 接続ポロジー（ネットワーク共有から動的生成）---
  const connections = useMemo(() => {
    const netIndex = {};
    topology.forEach(n => { if (!isSystemContainer(n.name)) netIndex[n.name] = new Set(n.networks); });
    const visible = containers.filter(c => !isSystemContainer(c.name));
    const result = [];
    for (const a of visible) {
      const la = getContainerGroup(a.name);
      if (!SRC_LAYERS.has(la)) continue;
      const na = netIndex[a.name];
      if (!na) continue;
      for (const b of visible) {
        if (a === b) continue;
        const lb = getContainerGroup(b.name);
        if (!DEP_LAYERS.has(lb) || la === lb) continue;
        const nb = netIndex[b.name];
        if (!nb) continue;
        const shared = [...na].filter(x => nb.has(x));
        if (shared.length > 0) result.push({ from: a.name, to: b.name, networks: shared });
      }
    }
    return result;
  }, [containers, topology]);

  const handleSelectContainer = (container) => { setSelectedContainer(container); const hasStorage = checkHasStorage(container); setActiveTab(hasStorage ? 'storage' : 'logs'); if (hasStorage) { setCurrentBucket(''); setCurrentPrefix(''); } };
  const handleCloseModal = () => setSelectedContainer(null);
  const checkHasStorage = useCallback((container) => container && (hasStorageMap[container.name] || ['minio', 'storage'].some(kw => (container.name || '').toLowerCase().includes(kw) || (container.image || '').toLowerCase().includes(kw))), [hasStorageMap]);
  
  const fetchItems = async () => { if (!selectedContainer) return; setLoading(true); setSelectedItem(null); try { const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix }); const res = await fetch(`/api/minio/list/${selectedContainer.name}?${params.toString()}`); const data = await res.json(); setItems(Array.isArray(data) ? data : []); } catch (err) { console.error(err); setItems([]); } finally { setLoading(false); } };
  useEffect(() => { if (selectedContainer && checkHasStorage(selectedContainer) && activeTab === 'storage') fetchItems(); }, [selectedContainer, currentBucket, currentPrefix, activeTab]);
  
  const currentLogs = selectedContainer ? logsMap[selectedContainer.name] || [] : [];
  useEffect(() => { logEndRef.current?.scrollIntoView({ behavior: 'smooth' }); }, [currentLogs]);
  
  const handleDoubleClick = (item) => { if (item.type === 'bucket') { setCurrentBucket(item.name); setCurrentPrefix(''); } else if (item.type === 'folder') { setCurrentPrefix(prev => `${prev}${item.name}/`); } };
  const handleGoBack = () => { if (currentPrefix) { const parts = currentPrefix.split('/').filter(Boolean); parts.pop(); setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : ''); } else if (currentBucket) setCurrentBucket(''); };
  const handleUpload = async (e) => { const file = e.target.files[0]; if (!file || !currentBucket) return; const formData = new FormData(); formData.append('file', file); const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix }); const res = await fetch(`/api/minio/upload/${selectedContainer.name}?${params.toString()}`, { method: 'POST', body: formData }); if (res.ok) fetchItems(); else alert('Upload failed'); };
  const handleDelete = async () => { if (!selectedItem || selectedItem.type !== 'file') return; if (!confirm(`Delete "${selectedItem.name}"?`)) return; const key = currentPrefix + selectedItem.name; const params = new URLSearchParams({ bucket: currentBucket, key }); const res = await fetch(`/api/minio/delete/${selectedContainer.name}?${params.toString()}`, { method: 'DELETE' }); if (res.ok) fetchItems(); else alert('Delete failed'); };
  const storageProps = { loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete, fetchItems };

  // システム全体の合計リソース使用率を計算
  const totalCpuUsage = useMemo(() => {
    let total = 0;
    let count = 0;
    for (const containerName in containerStats) {
      if (containerStats[containerName].cpuPercent !== undefined) {
        total += containerStats[containerName].cpuPercent;
        count++;
      }
    }
    return count > 0 ? total / count : 0;
  }, [containerStats]);

  const totalMemUsageGB = useMemo(() => {
    let totalUsage = 0;
    let totalLimit = 0;
    for (const containerName in containerStats) {
      if (containerStats[containerName].memUsage !== undefined && containerStats[containerName].memLimit !== undefined) {
        totalUsage += containerStats[containerName].memUsage;
        totalLimit += containerStats[containerName].memLimit;
      }
    }
    // バイトをGBに変換
    return {
      usage: totalUsage / (1024 * 1024 * 1024),
      limit: totalLimit / (1024 * 1024 * 1024)
    };
  }, [containerStats]);


  return (
    <div className="h-screen w-screen bg-slate-950 bg-cyber-grid text-slate-200 font-sans select-none overflow-auto">
      <CyberStyles />
      
      {/* HUD ヘッダー */}
      <div className="absolute top-0 left-0 p-4 z-20 bg-slate-950/80 backdrop-blur-md border-b border-r border-cyan-500/30 rounded-br-2xl">
        <h1 className="text-xl font-mono font-bold text-cyan-400 flex items-center gap-2 tracking-wider">
          <Waypoints className="animate-spin-slow" /> ATOMOSIDEA LAYERED SYSTEM MAP
        </h1>
        <p className="text-xs font-mono text-slate-500 mt-0.5">Top-to-Bottom Flowchart Topology</p>
      </div>

      <div className="absolute top-4 right-4 z-20 flex flex-col gap-4">
        <button onClick={() => setShowVolumes(true)} className="self-end text-xs font-mono px-3 py-1.5 rounded-lg bg-slate-900/80 backdrop-blur-md border border-cyan-500/30 text-cyan-400 hover:border-cyan-400 hover:text-cyan-300 transition-colors flex items-center gap-1.5"><HardDrive size={13} />Volumes</button>
        <AccessMonitor lps={lps} history={lpsHistory} />
        <UserCountMonitor count={activeUsers} />
        <SystemLoadMonitor 
          totalCpu={totalCpuUsage} 
          totalMemUsageGB={totalMemUsageGB.usage} 
          totalMemLimitGB={totalMemUsageGB.limit} 
        />
      </div>

      <div className="w-full h-full relative overflow-hidden" style={{ minWidth: canvasSize.width, minHeight: canvasSize.height }}>
        {/* SVG パケット接続線 */}
        <svg className="absolute top-0 left-0 w-full h-full" style={{ zIndex: 1 }}>
          {connections.map((conn, i) => (
            <ConnectionLine
              key={i}
              from={containerPositions[conn.from]}
              to={containerPositions[conn.to]}
              isActive={!!(activeLogsMap[conn.from] || activeLogsMap[conn.to])}
              hasError={!!(errorLogsMap[conn.from] || errorLogsMap[conn.to])}
              networks={conn.networks}
            />
          ))}
        </svg>

        {/* グリッドラベル（縦軸=サービス種別、横軸=カテゴリ）& ノードレンダリング */}
        <div className="relative w-full h-full z-10">
          {/* 縦軸ラベル（サービス種別） */}
          {layersLayout.rowOrder.map((row, i) => (
            <div
              key={`row-${row}`}
              className="absolute flex items-center gap-2 text-cyan-400/60 font-mono"
              style={{ top: `${84 + i * (96 + 12) - 8}px`, left: '16px', fontSize: '10px', fontWeight: 700, letterSpacing: '0.1em' }}
            >
              <h2 className="uppercase tracking-widest text-cyan-400/70">{groupLabelMap[row] || row}</h2>
            </div>
          ))}

          {/* 横軸ラベル（カテゴリ） */}
          {layersLayout.gridMeta.map(({ cat, x }) => (
            <div
              key={`cat-${cat}`}
              className="absolute flex items-center gap-2 text-cyan-400/70 font-mono"
              style={{ top: '4px', left: `${x - 30}px`, fontSize: '10px', fontWeight: 700, letterSpacing: '0.1em' }}
            >
              <h2 className="uppercase tracking-widest text-cyan-400/80">{cat}</h2>
            </div>
          ))}

          {containers.filter(c => !isSystemContainer(c.name)).map(c => (
            <ContainerNode
              key={c.id || c.name}
              container={c}
              onClick={handleSelectContainer}
              isActive={!!activeLogsMap[c.name]}
              hasError={!!(errorLogsMap[c.name] || c.health === 'unhealthy')}
              position={containerPositions[c.name]}
              stats={containerStats[c.name]}
              health={c.health}
              restartCount={c.restartCount}
              uptime={c.uptime}
            />
          ))}
        </div>
      </div>

      <LogViewerModal
        container={selectedContainer}
        onClose={handleCloseModal}
        logs={currentLogs}
        logEndRef={logEndRef}
        activeTab={activeTab}
        setActiveTab={setActiveTab}
        canShowStorage={checkHasStorage(selectedContainer)}
        storageProps={storageProps}
      />

      {showVolumes && <VolumesModal onClose={() => setShowVolumes(false)} volumes={volumes} />}
    </div>
  );
}