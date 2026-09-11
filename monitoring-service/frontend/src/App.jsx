import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import {
  Folder, FileText, Database, ChevronRight, ArrowLeft, Server, Upload, Trash2, RefreshCw, Terminal, HardDrive,
  RotateCw, Loader2, AlertTriangle, X, Cpu, GitBranch, Waypoints, Layers, Activity, Users
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
const ConnectionLine = ({ from, to, isActive, hasError }) => {
  if (!from || !to) return null;

  // 上から下への配線：送信元の「下端中央」 -> 送信先の「上端中央」
  const startX = from.x + 96;  // ノード幅 192px の中央
  const startY = from.y + 96;  // ノード高さ 96px の下端
  const endX = to.x + 96;
  const endY = to.y;           // ノードの上端

  // 縦方向の直角（Orthogonal）配線パス
  const midY = startY + (endY - startY) / 2;
  const d = `M ${startX} ${startY} L ${startX} ${midY} L ${endX} ${midY} L ${endX} ${endY}`;

  const glowColor = hasError ? '#ef4444' : '#06b6d4';

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
const ContainerNode = ({ container, onClick, isActive, hasError, position, stats }) => {
  const isRunning = container.state === 'running';
  const isStarting = container.state === 'starting' || container.state === 'restarting';

  const themeBorder = hasError
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

      <div className={`w-48 h-24 p-2.5 bg-slate-950/85 backdrop-blur-md border ${themeBorder} cyber-card hover:border-cyan-400 transition-all shadow-xl relative overflow-hidden flex flex-col justify-between`}>
        <div className="absolute inset-0 bg-[linear-gradient(rgba(18,16,16,0)_50%,rgba(0,0,0,0.3)_50%)] bg-[length:100%_4px] pointer-events-none opacity-40" />

        <div className="flex justify-between items-start relative z-10">
          <div className="text-xs font-mono font-bold text-slate-200 group-hover:text-cyan-300 truncate tracking-wide">
            {container.name}
          </div>
          <div className="flex items-center gap-1">
            {isStarting && <Loader2 className="w-3 h-3 text-amber-400 animate-spin" />}
            {isRunning && (
              <span className={`w-2 h-2 rounded-full ${isActive ? 'bg-cyan-400 shadow-[0_0_8px_#22d3ee]' : 'bg-slate-600'}`} />
            )}
          </div>
        </div>

        <div className="text-[10px] font-mono text-slate-500 truncate relative z-10 -mt-1">
          {container.image}
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

  useEffect(() => {
    fetchContainers();
    fetchActiveUsers();
    fetchContainerStats();
    const containerInterval = setInterval(fetchContainers, 3000);
    const statsInterval = setInterval(fetchContainerStats, 3000);
    const userInterval = setInterval(fetchActiveUsers, 10000);
    return () => {
      clearInterval(containerInterval);
      clearInterval(statsInterval);
      clearInterval(userInterval);
    };
  }, [fetchContainers, fetchActiveUsers, fetchContainerStats]);

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

  // --- 階層（縦方向 上→下）のレイアウト配置設定 ---
  const { containerPositions, connections, layersLayout } = useMemo(() => {
    // 上から順の階層定義 (row: 縦方向の位置)
    const layers = {
      'UI': { row: 0, title: '1. Frontend Layer' },
      'API': { row: 1, title: '2. API Gateway & Services (Backend)' },
      'Workers': { row: 2, title: '3. Security Scan & Processing Workers (Backend)' },
      'DB': { row: 3, title: '4. Databases & Cache' },
      'Storage': { row: 4, title: '5. Storage & Object Persistence' },
      'Unknown': { row: 5, title: 'Uncategorized' },
    };

    // フルネームや部分一致で柔軟に判定するグループ分けロジック
    const getContainerGroup = (name) => {
      const n = name.toLowerCase();
      
      // 1. UI Layer
      if (n.endsWith('frontend') || n.includes('frontend-builder')) return 'UI';

      // 2. API Services Layer
      if (
        n.endsWith('auth-service') || 
        n.endsWith('profile-service') || 
        n.endsWith('upload-service') || 
        n.endsWith('stream-service') || 
        n.endsWith('game-upload-api') || 
        n.endsWith('static-site-upload-api') || 
        n.endsWith('mypage-service')
      ) return 'API';

      // 3. Workers & Security Scan Layer
      if (
        n.endsWith('sfsp-api') || 
        n.endsWith('sfsp-worker') || 
        n.endsWith('video-worker') || 
        n.endsWith('game-worker') || 
        n.endsWith('static-site-worker') ||
        n.includes('clamav') || 
        n.includes('yara')
      ) return 'Workers';

      // 4. DB & Cache Layer
      if (
        n.endsWith('auth-db') || 
        n.endsWith('app-db') || 
        n.endsWith('profile-db') || 
        n.endsWith('sfsp-db') || 
        n.endsWith('redis')
      ) return 'DB';

      // 5. Storage Layer
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

    // 接続定義（前方・部分一致でノードを探せるように判定側で吸収）
    const rawConnections = [
      ['frontend', 'auth-service'], ['frontend', 'profile-service'], ['frontend', 'upload-service'],
      ['frontend', 'stream-service'], ['frontend', 'game-upload-api'], ['frontend', 'static-site-upload-api'],
      ['frontend', 'mypage-service'], ['frontend', 'video-storage'],

      ['auth-service', 'auth-db'], ['auth-service', 'app-db'], ['auth-service', 'profile-db'],
      ['auth-service', 'redis'], ['auth-service', 'profile-storage'], ['auth-service', 'game-storage'],
      ['auth-service', 'static-site-storage'], ['auth-service', 'profile-service'],

      ['profile-service', 'profile-db'], ['profile-service', 'profile-storage'],

      ['upload-service', 'app-db'], ['upload-service', 'redis'], ['upload-service', 'sfsp-api'], ['upload-service', 'video-storage'],
      ['game-upload-api', 'app-db'], ['game-upload-api', 'redis'], ['game-upload-api', 'sfsp-api'], ['game-upload-api', 'game-storage'],
      ['static-site-upload-api', 'app-db'], ['static-site-upload-api', 'redis'], ['static-site-upload-api', 'sfsp-api'], ['static-site-upload-api', 'static-site-storage'],

      ['stream-service', 'app-db'], ['mypage-service', 'auth-db'], ['mypage-service', 'app-db'],

      ['sfsp-api', 'sfsp-db'], ['sfsp-api', 'sfsp-minio'], ['sfsp-api', 'redis'],
      ['sfsp-worker', 'redis'], ['sfsp-worker', 'sfsp-db'], ['sfsp-worker', 'sfsp-minio'],

      ['video-worker', 'redis'], ['video-worker', 'app-db'], ['video-worker', 'sfsp-minio'], ['video-worker', 'video-storage'],
      ['game-worker', 'redis'], ['game-worker', 'app-db'], ['game-worker', 'sfsp-minio'], ['game-worker', 'game-storage'],
      ['static-site-worker', 'redis'], ['static-site-worker', 'app-db'], ['static-site-worker', 'sfsp-minio'], ['static-site-worker', 'static-site-storage'],
    ];

    const colWidth = 210;
    const rowHeight = 160;
    const positions = {};

    const groupedContainers = containers.reduce((acc, c) => {
      const group = getContainerGroup(c.name);
      if (!acc[group]) acc[group] = [];
      acc[group].push(c);
      return acc;
    }, {});

    Object.keys(groupedContainers).forEach(group => {
      const row = layers[group]?.row ?? 5;
      groupedContainers[group].forEach((c, i) => {
        positions[c.name] = { x: i * colWidth + 80, y: row * rowHeight + 110 };
      });
    });

    // 接続線の名前をコンテナの実際の名前（atmosidea-等が付いた名前）に展開・結線
    const activeConnections = [];
    rawConnections.forEach(([fromKey, toKey]) => {
      const realFrom = containers.find(c => c.name.endsWith(fromKey) || c.name === fromKey)?.name;
      const realTo = containers.find(c => c.name.endsWith(toKey) || c.name === toKey)?.name;
      if (realFrom && realTo) {
        activeConnections.push([realFrom, realTo]);
      }
    });

    return { containerPositions: positions, connections: activeConnections, layersLayout: { layers, groupedContainers, rowHeight } };
  }, [containers]);

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
        <AccessMonitor lps={lps} history={lpsHistory} />
        <UserCountMonitor count={activeUsers} />
        <SystemLoadMonitor 
          totalCpu={totalCpuUsage} 
          totalMemUsageGB={totalMemUsageGB.usage} 
          totalMemLimitGB={totalMemUsageGB.limit} 
        />
      </div>

      <div className="w-full h-full relative min-w-[1600px] min-h-[1050px]">
        {/* SVG パケット接続線 */}
        <svg className="absolute top-0 left-0 w-full h-full" style={{ zIndex: 1 }}>
          {connections.map(([from, to], i) => (
            <ConnectionLine
              key={i}
              from={containerPositions[from]}
              to={containerPositions[to]}
              isActive={!!(activeLogsMap[from] || activeLogsMap[to])}
              hasError={!!(errorLogsMap[from] || errorLogsMap[to])}
            />
          ))}
        </svg>

        {/* 階層ラベル & ノードレンダリング */}
        <div className="relative w-full h-full z-10">
          {Object.keys(layersLayout.layers).map(group => {
            const row = layersLayout.layers[group].row;
            return (
              <div
                key={group}
                className="absolute left-8 flex items-center gap-2 text-cyan-400/80 border-b border-cyan-500/20 pb-1 pr-6"
                style={{ top: `${row * layersLayout.rowHeight + 80}px` }}
              >
                <Layers size={14} />
                <h2 className="font-mono text-xs font-bold uppercase tracking-widest">
                  {layersLayout.layers[group].title}
                </h2>
              </div>
            );
          })}

          {containers.map(c => containerPositions[c.name] && (
            <ContainerNode
              key={c.id || c.name}
              container={c}
              onClick={handleSelectContainer}
              isActive={!!activeLogsMap[c.name]}
              hasError={!!errorLogsMap[c.name]}
              position={containerPositions[c.name]}
              stats={containerStats[c.name]}
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
    </div>
  );
}
