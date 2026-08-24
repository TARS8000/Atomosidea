import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  Folder,
  FileText,
  Database,
  ChevronRight,
  ArrowLeft,
  Server,
  Upload,
  Trash2,
  RefreshCw,
  Terminal,
  HardDrive,
  RotateCw,
  Loader2,
  AlertTriangle
} from 'lucide-react';

export default function App() {
  const [containers, setContainers] = useState([]);
  const [selectedContainer, setSelectedContainer] = useState(null);
  const [activeTab, setActiveTab] = useState('logs');

  // MinIO エクスプローラー用
  const [currentBucket, setCurrentBucket] = useState('');
  const [currentPrefix, setCurrentPrefix] = useState('');
  const [items, setItems] = useState([]);
  const [selectedItem, setSelectedItem] = useState(null);
  const [loading, setLoading] = useState(false);

  // ログ保持・全コンテナ点滅・エラー状態管理用
  const [logsMap, setLogsMap] = useState({});
  const [activeLogsMap, setActiveLogsMap] = useState({});
  const [errorLogsMap, setErrorLogsMap] = useState({});
  const [hasStorageMap, setHasStorageMap] = useState({});

  const logEndRef = useRef(null);
  const wsMapRef = useRef({});
  const pulseTimeoutsRef = useRef({});
  const reconnectTimeoutsRef = useRef({});

  const [restarting, setRestarting] = useState(false);
  const fileInputRef = useRef(null);

  // ストレージ機能の判定
  const checkHasStorage = useCallback((container) => {
    if (!container) return false;
    if (hasStorageMap[container.name]) return true;
    if (typeof container.hasStorage === 'boolean') return container.hasStorage;
    if (typeof container.isStorage === 'boolean') return container.isStorage;

    const name = (container.name || '').toLowerCase();
    const image = (container.image || '').toLowerCase();
    const storageKeywords = ['minio', 'storage', 's3', 'bucket', 'oss', 'blob'];

    return storageKeywords.some((kw) => name.includes(kw) || image.includes(kw));
  }, [hasStorageMap]);

  // コンテナ一覧取得（3秒間隔）
  const fetchContainers = useCallback(async () => {
    try {
      const res = await fetch('/api/containers');
      if (!res.ok) throw new Error('Failed to fetch containers');
      const data = await res.json();
      setContainers(data);

      setSelectedContainer((prevSelected) => {
        if (!prevSelected && data.length > 0) return data[0];
        if (prevSelected) {
          const updated = data.find((c) => c.id === prevSelected.id || c.name === prevSelected.name);
          return updated || data[0] || null;
        }
        return null;
      });
    } catch (err) {
      console.error('Fetch containers error:', err);
    }
  }, []);

  useEffect(() => {
    fetchContainers();
    const interval = setInterval(fetchContainers, 3000);
    return () => clearInterval(interval);
  }, [fetchContainers]);

  // コンテナ選択処理
  const selectContainer = (container) => {
    setSelectedContainer(container);
    setCurrentBucket('');
    setCurrentPrefix('');

    if (checkHasStorage(container)) {
      setActiveTab('storage');
    } else {
      setActiveTab('logs');
    }
  };

  // WebSocket の接続管理
  useEffect(() => {
    if (containers.length === 0) return;

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';

    containers.forEach((c) => {
      const isLive = c.state === 'running' || c.state === 'starting' || c.state === 'restarting';

      if (!isLive) {
        if (wsMapRef.current[c.name]) {
          wsMapRef.current[c.name].close();
          delete wsMapRef.current[c.name];
        }
        return;
      }

      const connectWs = () => {
        if (
            wsMapRef.current[c.name] &&
            (wsMapRef.current[c.name].readyState === WebSocket.OPEN ||
                wsMapRef.current[c.name].readyState === WebSocket.CONNECTING)
        ) {
          return;
        }

        const wsUrl = `${protocol}//${window.location.host}/ws/logs?container=${c.name}`;
        const socket = new WebSocket(wsUrl);

        socket.onopen = () => {
          if (reconnectTimeoutsRef.current[c.name]) {
            clearTimeout(reconnectTimeoutsRef.current[c.name]);
            delete reconnectTimeoutsRef.current[c.name];
          }
        };

        socket.onmessage = (event) => {
          const logData = event.data;

          if (logData.includes('docs.min.io') || logData.includes('min.io')) {
            setHasStorageMap((prev) => ({ ...prev, [c.name]: true }));
          }

          const isErrorLog = /(error|fatal|fail|exception|stderr|panic)/i.test(logData);
          if (isErrorLog) {
            setErrorLogsMap((prev) => ({ ...prev, [c.name]: true }));
          }

          setLogsMap((prev) => ({
            ...prev,
            [c.name]: [...(prev[c.name] || []), logData],
          }));

          setActiveLogsMap((prev) => ({ ...prev, [c.name]: true }));

          if (pulseTimeoutsRef.current[c.name]) {
            clearTimeout(pulseTimeoutsRef.current[c.name]);
          }
          pulseTimeoutsRef.current[c.name] = setTimeout(() => {
            setActiveLogsMap((prev) => ({ ...prev, [c.name]: false }));
          }, 300);
        };

        socket.onclose = () => {
          delete wsMapRef.current[c.name];
          reconnectTimeoutsRef.current[c.name] = setTimeout(() => {
            connectWs();
          }, 3000);
        };

        socket.onerror = (err) => {
          console.error(`WebSocket Error [${c.name}]:`, err);
          setErrorLogsMap((prev) => ({ ...prev, [c.name]: true }));
          socket.close();
        };

        wsMapRef.current[c.name] = socket;
      };

      connectWs();
    });

    Object.keys(wsMapRef.current).forEach((name) => {
      const exists = containers.some((c) =>
          c.name === name && (c.state === 'running' || c.state === 'starting' || c.state === 'restarting')
      );
      if (!exists) {
        if (reconnectTimeoutsRef.current[name]) {
          clearTimeout(reconnectTimeoutsRef.current[name]);
          delete reconnectTimeoutsRef.current[name];
        }
        wsMapRef.current[name]?.close();
        delete wsMapRef.current[name];
      }
    });
  }, [containers]);

  useEffect(() => {
    return () => {
      Object.values(wsMapRef.current).forEach((ws) => ws.close());
      Object.values(pulseTimeoutsRef.current).forEach((t) => clearTimeout(t));
      Object.values(reconnectTimeoutsRef.current).forEach((t) => clearTimeout(t));
    };
  }, []);

  const handleRestartContainer = async () => {
    if (!selectedContainer || restarting) return;
    if (!confirm(`コンテナ「${selectedContainer.name}」を再起動しますか？`)) return;

    setRestarting(true);
    setErrorLogsMap((prev) => ({ ...prev, [selectedContainer.name]: false }));

    try {
      const res = await fetch(`/api/containers/restart/${selectedContainer.name}`, {
        method: 'POST',
      });
      if (res.ok) {
        fetchContainers();
      } else {
        alert('再起動に失敗しました');
      }
    } catch (err) {
      console.error(err);
      alert('エラーが発生しました');
    } finally {
      setRestarting(false);
    }
  };

  const canShowStorage = checkHasStorage(selectedContainer);

  useEffect(() => {
    if (selectedContainer && canShowStorage && activeTab === 'storage') {
      fetchItems();
    }
  }, [selectedContainer, currentBucket, currentPrefix, activeTab, canShowStorage]);

  const fetchItems = async () => {
    if (!selectedContainer) return;
    setLoading(true);
    setSelectedItem(null);
    try {
      const params = new URLSearchParams();
      if (currentBucket) params.append('bucket', currentBucket);
      if (currentPrefix) params.append('prefix', currentPrefix);

      const res = await fetch(`/api/minio/list/${selectedContainer.name}?${params.toString()}`);
      if (!res.ok) {
        setItems([]);
        return;
      }
      const data = await res.json();
      setItems(Array.isArray(data) ? data : []);
    } catch (err) {
      console.error(err);
      setItems([]);
    } finally {
      setLoading(false);
    }
  };

  const currentLogs = selectedContainer ? logsMap[selectedContainer.name] || [] : [];
  useEffect(() => {
    if (activeTab === 'logs') {
      logEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [currentLogs, activeTab]);

  const handleDoubleClick = (item) => {
    if (item.type === 'bucket') {
      setCurrentBucket(item.name);
      setCurrentPrefix('');
    } else if (item.type === 'folder') {
      setCurrentPrefix((prev) => `${prev}${item.name}/`);
    }
  };

  const handleGoBack = () => {
    if (currentPrefix) {
      const parts = currentPrefix.split('/').filter(Boolean);
      parts.pop();
      setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : '');
    } else if (currentBucket) {
      setCurrentBucket('');
    }
  };

  const handleUpload = async (e) => {
    const file = e.target.files[0];
    if (!file || !currentBucket) return;

    const formData = new FormData();
    formData.append('file', file);

    const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix });
    try {
      const res = await fetch(`/api/minio/upload/${selectedContainer.name}?${params.toString()}`, {
        method: 'POST',
        body: formData,
      });
      if (res.ok) {
        fetchItems();
      } else {
        alert('アップロードに失敗しました');
      }
    } catch (err) {
      alert('エラーが発生しました');
    }
  };

  const handleDelete = async () => {
    if (!selectedItem || selectedItem.type !== 'file') return;
    if (!confirm(`「${selectedItem.name}」を削除しますか？`)) return;

    const key = currentPrefix + selectedItem.name;
    const params = new URLSearchParams({ bucket: currentBucket, key });

    try {
      const res = await fetch(`/api/minio/delete/${selectedContainer.name}?${params.toString()}`, {
        method: 'DELETE',
      });
      if (res.ok) {
        fetchItems();
      } else {
        alert('削除に失敗しました');
      }
    } catch (err) {
      alert('エラーが発生しました');
    }
  };

  const isSelectedContainerActive = selectedContainer ? !!activeLogsMap[selectedContainer.name] : false;
  const isSelectedContainerHasError = selectedContainer ? !!errorLogsMap[selectedContainer.name] : false;

  const isSelectedContainerStarting = selectedContainer
      ? (selectedContainer.state === 'starting' || selectedContainer.state === 'restarting' || restarting)
      : false;

  const isRestartDisabled = !selectedContainer || selectedContainer.state !== 'running' || restarting;

  return (
      <div className="flex h-screen bg-slate-900 text-slate-200 font-sans select-none">
        {/* サイドバー */}
        <div className="w-80 border-r border-slate-800 bg-slate-950 flex flex-col">
          <div className="p-4 border-b border-slate-800 flex items-center justify-between">
            <div className="flex items-center gap-2 font-bold text-blue-400">
              <Server className="w-5 h-5" />
              <span>Containers</span>
            </div>
            <button onClick={fetchContainers} className="p-1.5 hover:bg-slate-800 rounded text-slate-400">
              <RefreshCw className="w-4 h-4" />
            </button>
          </div>

          <div className="flex-1 overflow-y-auto p-2 space-y-1">
            {containers.map((c) => {
              const isSelected = selectedContainer?.id === c.id;
              const isRunning = c.state === 'running';
              const isStarting = c.state === 'starting' || c.state === 'restarting';
              const isActive = !!activeLogsMap[c.name];
              const hasError = !!errorLogsMap[c.name];

              return (
                  <div
                      key={c.id}
                      onClick={() => selectContainer(c)}
                      className={`p-3 rounded-lg cursor-pointer border text-sm transition ${
                          isSelected
                              ? 'bg-blue-600/20 border-blue-500 text-white font-medium'
                              : 'border-transparent hover:bg-slate-900 text-slate-400'
                      }`}
                  >
                    <div className="flex justify-between items-center">
                      <span className="truncate">{c.name}</span>

                      <div className="flex items-center gap-1.5">
                        {isStarting && (
                            <Loader2 className="w-3.5 h-3.5 text-amber-400 animate-spin" />
                        )}

                        {isRunning && (
                            hasError ? (
                                <span className="w-2.5 h-2.5 rounded-full bg-rose-500 animate-ping shadow-[0_0_8px_#f43f5e]" title="エラー検知" />
                            ) : (
                                <span
                                    className={`w-2 h-2 rounded-full transition-all duration-150 ${
                                        isActive
                                            ? 'bg-emerald-400 scale-125 shadow-[0_0_8px_#34d399]'
                                            : 'bg-emerald-600/30'
                                    }`}
                                />
                            )
                        )}

                        <span
                            className={`text-[10px] px-2 py-0.5 rounded-full ${
                                hasError
                                    ? 'bg-rose-500/20 text-rose-400 border border-rose-500/30 font-semibold'
                                    : isRunning
                                        ? 'bg-emerald-500/20 text-emerald-400'
                                        : isStarting
                                            ? 'bg-amber-500/20 text-amber-400 font-semibold'
                                            : 'bg-slate-800 text-slate-500'
                            }`}
                        >
                      {c.state}
                    </span>
                      </div>
                    </div>
                  </div>
              );
            })}
          </div>
        </div>

        {/* メインエリア */}
        <div className="flex-1 flex flex-col bg-slate-900 overflow-hidden">
          {selectedContainer ? (
              <>
                <div className="p-4 border-b border-slate-800 bg-slate-950 flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <h1 className="font-bold text-lg text-slate-100 flex items-center gap-2">
                      {selectedContainer.name}
                      {isSelectedContainerHasError && (
                          <span className="text-xs font-normal text-rose-400 flex items-center gap-1 bg-rose-500/10 px-2 py-0.5 rounded-full border border-rose-500/20">
                      <AlertTriangle className="w-3 h-3 text-rose-400" />
                      Error Detected
                    </span>
                      )}
                      {isSelectedContainerStarting && (
                          <span className="text-xs font-normal text-amber-400 flex items-center gap-1 bg-amber-500/10 px-2 py-0.5 rounded-full border border-amber-500/20">
                      <Loader2 className="w-3 h-3 animate-spin" />
                      起動中...
                    </span>
                      )}
                    </h1>

                    <button
                        onClick={handleRestartContainer}
                        disabled={isRestartDisabled}
                        className="flex items-center gap-1.5 text-xs bg-slate-800 hover:bg-slate-700 text-slate-300 border border-slate-700 px-2.5 py-1 rounded-md transition disabled:opacity-40 disabled:cursor-not-allowed"
                        title={isRestartDisabled ? 'コンテナが稼働中(running)の時のみ再起動できます' : 'コンテナを再起動'}
                    >
                      <RotateCw className={`w-3.5 h-3.5 ${isSelectedContainerStarting ? 'animate-spin' : ''}`} />
                      {isSelectedContainerStarting ? '起動処理中...' : '再起動'}
                    </button>
                  </div>

                  <div className="flex bg-slate-900 p-1 rounded-lg border border-slate-800 text-xs">
                    {canShowStorage && (
                        <button
                            onClick={() => setActiveTab('storage')}
                            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition ${
                                activeTab === 'storage'
                                    ? 'bg-blue-600 text-white font-medium'
                                    : 'text-slate-400 hover:text-slate-200'
                            }`}
                        >
                          <HardDrive className="w-3.5 h-3.5" />
                          Storage
                        </button>
                    )}
                    <button
                        onClick={() => setActiveTab('logs')}
                        className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md transition relative ${
                            activeTab === 'logs' || !canShowStorage
                                ? 'bg-blue-600 text-white font-medium'
                                : 'text-slate-400 hover:text-slate-200'
                        }`}
                    >
                      <Terminal className="w-3.5 h-3.5" />
                      Logs
                      <span
                          className={`w-2 h-2 rounded-full transition-all duration-150 ${
                              isSelectedContainerHasError
                                  ? 'bg-rose-500 shadow-[0_0_8px_#f43f5e]'
                                  : isSelectedContainerActive
                                      ? 'bg-emerald-400 scale-125 shadow-[0_0_8px_#34d399]'
                                      : 'bg-emerald-600/40'
                          }`}
                      />
                    </button>
                  </div>
                </div>

                {/* コンテンツエリア */}
                <div className="flex-1 flex flex-col p-4 overflow-hidden">
                  {canShowStorage && activeTab === 'storage' ? (
                      <div className="flex-1 bg-slate-950 border border-slate-800 rounded-xl flex flex-col overflow-hidden">
                        <div className="p-2 border-b border-slate-800 bg-slate-900/50 flex items-center justify-between gap-2">
                          <div className="flex items-center gap-2">
                            <button
                                onClick={handleGoBack}
                                disabled={!currentBucket && !currentPrefix}
                                className="p-1.5 hover:bg-slate-800 rounded disabled:opacity-30 text-slate-300"
                                title="上の階層へ"
                            >
                              <ArrowLeft className="w-4 h-4" />
                            </button>

                            <div className="flex items-center gap-1 bg-slate-900 border border-slate-800 px-3 py-1 rounded text-xs text-slate-300 font-mono">
                              <span onClick={() => { setCurrentBucket(''); setCurrentPrefix(''); }} className="cursor-pointer hover:underline text-blue-400">Root</span>
                              {currentBucket && (
                                  <>
                                    <ChevronRight className="w-3 h-3 text-slate-600" />
                                    <span onClick={() => setCurrentPrefix('')} className="cursor-pointer hover:underline text-blue-400">{currentBucket}</span>
                                  </>
                              )}
                              {currentPrefix.split('/').filter(Boolean).map((p, i) => (
                                  <React.Fragment key={i}>
                                    <ChevronRight className="w-3 h-3 text-slate-600" />
                                    <span>{p}</span>
                                  </React.Fragment>
                              ))}
                            </div>
                          </div>

                          {currentBucket && (
                              <div className="flex items-center gap-2">
                                <input type="file" ref={fileInputRef} onChange={handleUpload} className="hidden" />
                                <button
                                    onClick={() => fileInputRef.current?.click()}
                                    className="flex items-center gap-1.5 text-xs bg-blue-600 hover:bg-blue-500 text-white px-3 py-1.5 rounded transition font-medium"
                                >
                                  <Upload className="w-3.5 h-3.5" />
                                  アップロード
                                </button>

                                <button
                                    onClick={handleDelete}
                                    disabled={!selectedItem || selectedItem.type !== 'file'}
                                    className="flex items-center gap-1.5 text-xs bg-rose-600/20 hover:bg-rose-600 border border-rose-500/30 text-rose-400 hover:text-white px-3 py-1.5 rounded transition disabled:opacity-30"
                                >
                                  <Trash2 className="w-3.5 h-3.5" />
                                  削除
                                </button>
                              </div>
                          )}
                        </div>

                        <div className="flex-1 overflow-y-auto">
                          <table className="w-full text-left text-xs border-collapse">
                            <thead>
                            <tr className="border-b border-slate-800 text-slate-500 bg-slate-900/30 sticky top-0">
                              <th className="p-2.5 pl-4 font-medium">名前</th>
                              <th className="p-2.5 font-medium w-24">種類</th>
                              <th className="p-2.5 font-medium w-28 text-right pr-4">サイズ</th>
                            </tr>
                            </thead>
                            <tbody>
                            {loading ? (
                                <tr><td colSpan="3" className="p-8 text-center text-slate-600">読み込み中...</td></tr>
                            ) : items.length === 0 ? (
                                <tr><td colSpan="3" className="p-8 text-center text-slate-600">利用可能なストレージがないか、空のフォルダです</td></tr>
                            ) : (
                                items.map((item, index) => {
                                  const isSelected = selectedItem?.name === item.name;
                                  return (
                                      <tr
                                          key={index}
                                          onClick={() => setSelectedItem(item)}
                                          onDoubleClick={() => handleDoubleClick(item)}
                                          className={`border-b border-slate-800/40 cursor-pointer transition ${
                                              isSelected ? 'bg-blue-600/30 text-white' : 'hover:bg-slate-900/80 text-slate-300'
                                          }`}
                                      >
                                        <td className="p-2 pl-4 flex items-center gap-2 font-medium">
                                          {item.type === 'bucket' && <Database className="w-4 h-4 text-emerald-400" />}
                                          {item.type === 'folder' && <Folder className="w-4 h-4 text-amber-400" />}
                                          {item.type === 'file' && <FileText className="w-4 h-4 text-blue-400" />}
                                          <span>{item.name}</span>
                                        </td>
                                        <td className="p-2 capitalize text-slate-500">{item.type}</td>
                                        <td className="p-2 pr-4 text-right font-mono text-slate-500">
                                          {item.size ? `${(item.size / 1024).toFixed(1)} KB` : '-'}
                                        </td>
                                      </tr>
                                  );
                                })
                            )}
                            </tbody>
                          </table>
                        </div>
                      </div>
                  ) : (
                      /* ログ表示エリア（コピー可能なテキスト選択 select-text を付与） */
                      <div className="flex-1 bg-slate-950 border border-slate-800 rounded-xl p-4 overflow-y-auto font-mono text-xs space-y-1 select-text selection:bg-blue-500 selection:text-white">
                        {currentLogs.length === 0 ? (
                            <div className="text-slate-600 italic select-none">ログを受信中、またはログが存在しません...</div>
                        ) : (
                            currentLogs.map((log, index) => {
                              const isLastLine = index === currentLogs.length - 1;
                              const isErr = /(error|fatal|fail|exception|stderr|panic)/i.test(log);

                              // 色付け条件: エラーなら赤、最新行なら緑、それ以外は通常色
                              let textColorClass = 'text-slate-300';
                              if (isErr) {
                                textColorClass = 'text-rose-400 bg-rose-950/30 font-semibold px-1 rounded';
                              } else if (isLastLine) {
                                textColorClass = 'text-emerald-400 font-semibold';
                              }

                              return (
                                  <div
                                      key={index}
                                      className={`log-line whitespace-pre-wrap break-all ${textColorClass}`}
                                  >
                                    {log}
                                  </div>
                              );
                            })
                        )}
                        <div ref={logEndRef} />
                      </div>
                  )}
                </div>
              </>
          ) : (
              <div className="flex-1 flex items-center justify-center text-slate-600">
                コンテナを選択してください
              </div>
          )}
        </div>
      </div>
  );
}