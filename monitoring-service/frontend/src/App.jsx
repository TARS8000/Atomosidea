import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import {
  Folder, FileText, Database, ChevronRight, ChevronDown, ArrowLeft, Upload, Trash2, HardDrive,
  X, Activity, Users, Cpu, Search, Boxes, AlertTriangle, RotateCcw, ShieldAlert, ListOrdered
} from 'lucide-react';

// --- 分類 ---

// isSystemContainer は監視対象外のコンテナかどうかを返す。
// 監視対象外: 監視サービス自身（mon-*）と、ビルド後に終了するビルダー（*-builder）
// （他プロジェクトのコンテナは、バックエンドが Atomosidea のプロジェクトに絞って返すので、ここでは扱わない）
const isSystemContainer = (name) => (name || '').startsWith('mon-') || /-builder$/.test(name);

// compose のサービス名（ラベルが無いコンテナは名前から atmosidea- を外したもの）
const serviceName = (c) => c.service || c.name.replace(/^atmosidea-/i, '');

// レイヤー（フローチャートの段）。上から下へ、リクエストが流れる順に並べる
const LAYERS = [
  { key: 'frontend', label: 'Entry', sub: 'Frontend' },
  { key: 'api', label: 'API', sub: 'HTTP / Upload' },
  { key: 'worker', label: 'Worker', sub: '非同期処理' },
  { key: 'db', label: 'DB/Cache', sub: 'PostgreSQL・Redis' },
  { key: 'storage', label: 'Storage', sub: 'MinIO' },
  { key: 'infra', label: 'Infra', sub: '初期化・補助' },
];

// getLayer はコンテナが属するレイヤー（段）のキーを返す。上から順に判定するため、条件の順序に意味がある
// （例: "minio-init" は storage より先に infra として扱う。"xxx-upload-api" と "sfsp-api" は API の段に置く）
const getLayer = (c) => {
  const s = serviceName(c).toLowerCase();
  if (s === 'frontend' || s.endsWith('-frontend')) return 'frontend';
  if (s.endsWith('-init') || s.includes('docker-proxy') || s.includes('docker-guard')) return 'infra';
  if (s.endsWith('-api') || s.endsWith('-service')) return 'api';
  if (s.endsWith('-worker') || s.includes('clamav') || s.includes('yara')) return 'worker';
  if (s.endsWith('-db') || s === 'redis') return 'db';
  if (s.includes('minio') || s.includes('storage')) return 'storage';
  return 'infra';
};

// 機能（フローチャートの縦の列 = スイムレーン）。サービス名の接頭辞で判定し、どれにも当たらないもの（minio・redis・app-db・init 系）は共通基盤
const FEATURES = ['auth', 'profile', 'team', 'mypage', 'game', 'video', 'static-site', 'sfsp'];
const SHARED_FEATURE = '共通基盤';

// getFeature はコンテナが属する機能名を返す。サービス名が機能名と一致するか「機能名-」で始まれば、その機能とみなす
const getFeature = (c) => {
  const s = serviceName(c).toLowerCase();
  return FEATURES.find((f) => s === f || s.startsWith(`${f}-`)) ?? SHARED_FEATURE;
};

// shortName はカードに出す短い名前。レーンの見出しと重なる機能名の接頭辞を外す（例: game レーンの "game-upload-api" → "upload-api"）
const shortName = (c) => {
  const s = serviceName(c);
  const f = getFeature(c);
  return f !== SHARED_FEATURE && s.toLowerCase().startsWith(`${f}-`) ? s.slice(f.length + 1) : s;
};

// --- 状態 ---

// isCompleted は exit 0 で終わった一回きりのコンテナ（init 系など）かどうかを返す。異常ではない
// Docker の status 文字列（例: "Exited (0) 5 minutes ago"）に "(0)" が含まれるかで判定する
const isCompleted = (c) => c.state === 'exited' && /\(0\)/.test(c.status || '');

// 状態ごとの表示スタイル
// dot: 状態ランプのクラス / text: 文字色のクラス / color: 発光やカード左端の帯の色 / frame: カード枠の色 / label: HUD 表記
const STATUS_STYLE = {
  error: { dot: 'bg-rose-500 shadow-[0_0_8px_#f43f5e] animate-pulse', text: 'text-rose-400', color: '#f43f5e', frame: 'rgba(244, 63, 94, 0.9)', label: 'ALERT' },
  down: { dot: 'bg-rose-500 shadow-[0_0_6px_#f43f5e]', text: 'text-rose-400', color: '#f43f5e', frame: 'rgba(244, 63, 94, 0.55)', label: 'OFFLINE' },
  warn: { dot: 'bg-amber-400 shadow-[0_0_6px_#fbbf24]', text: 'text-amber-300', color: '#fbbf24', frame: 'rgba(251, 191, 36, 0.6)', label: 'STANDBY' },
  ok: { dot: 'bg-emerald-400 shadow-[0_0_6px_#34d399]', text: 'text-emerald-300', color: '#34d399', frame: 'rgba(34, 211, 238, 0.4)', label: 'ONLINE' },
  done: { dot: 'border border-slate-400', text: 'text-slate-400', color: '#475569', frame: 'rgba(100, 116, 139, 0.4)', label: 'COMPLETE' },
};
// 状態の深刻度。数値が大きいほど深刻で、レーンの集約や「要確認」一覧の並び順に使う
const SEVERITY = { error: 4, down: 3, warn: 2, ok: 1, done: 0 };

// getStatus はコンテナの表示上の状態を返す。hasError はエラーログ多発を検出済みかどうか。
// error: unhealthy またはエラーログ多発 / down: 異常終了 / warn: 起動中・一時停止など / ok: 稼働中 / done: 正常終了
const getStatus = (c, hasError) => {
  if (hasError || c.health === 'unhealthy') return 'error';
  if (c.state === 'running') return c.health === 'starting' ? 'warn' : 'ok';
  if (isCompleted(c)) return 'done';
  if (c.state === 'exited' || c.state === 'dead') return 'down';
  return 'warn';
};

// worstStatus は状態の配列のうち最も深刻なものを返す（空配列なら 'done'）
const worstStatus = (statuses) => statuses.reduce((w, s) => (SEVERITY[s] > SEVERITY[w] ? s : w), 'done');

// エラー行とみなすログの判定パターン（大文字小文字を区別しない部分一致のため、"0 errors" のような行も該当する）
const ERROR_LINE = /(error|fatal|fail|exception|stderr|panic)/i;
// エラー検出後、赤い強調表示を保持してから状態をリセットするまでの時間（ms）
const ERROR_HIGHLIGHT_MS = 10000;
// コンテナごとに保持するログの最大行数（古い行から捨てる）
const MAX_LOG_LINES = 500;
// WebSocket の接続直後にまとめて届く過去ログ（直近 100 行）とみなす時間（ms）。
// この間に届いた行は表示するが、エラー検出・LPS・ログ出力中の印には数えない（開いた直後の誤検出を防ぐ）
const BACKLOG_MS = 1000;

// formatBytes はバイト数を 1GB 以上なら GB（小数1桁）、それ未満なら MB（整数）の文字列にする。0 や未定義は '-'
const formatBytes = (b) => {
  if (!b) return '-';
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(1)}GB`;
  return `${Math.round(b / 1024 ** 2)}MB`;
};

// pad2 は数値を 2 桁の 0 埋め文字列にする（時計や段の番号の表示用）
const pad2 = (n) => String(n).padStart(2, '0');

// --- フローチャートの配置（縦型） ---

// 配置の寸法（px）。gutter: 左端の段の名前の幅 / colGap: レーン同士の間隔 / laneHeaderH: レーン見出しの帯の高さ
// rowPad: 段の中の上下余白（同じ段どうしをつなぐ矢印もここを通る） / cellGap: 同じセルに横並びするカードの間隔
const FLOW = { gutter: 124, cardW: 150, cardH: 58, colGap: 16, laneHeaderH: 46, rowPad: 16, cellGap: 10, padRight: 16, padBottom: 20 };
// 画面幅に合わせて縮小するときの下限。これより狭いと縮小をやめて横スクロールにする（小さくしすぎると読めないため）
const MIN_SCALE = 0.8;

// buildLayout は表示するコンテナから縦型フローチャートの配置を求める。
// 段 = レイヤー（上から Entry → API → Worker → DB/Cache → Storage → Infra。コンテナが無い段は詰める）、列 = 機能のレーン。
// 同じレーン・同じ段のコンテナは横に並べ、レーンの幅は一番多く並ぶセルに合わせる。
// Frontend はどのレーンにも属さない入口として最上段の中央に置き、その下にレーンの見出しの帯を置く
const buildLayout = (nodes) => {
  const entry = nodes.filter((c) => getLayer(c) === 'frontend');
  const rest = nodes.filter((c) => getLayer(c) !== 'frontend');
  const laneKeys = [...FEATURES, SHARED_FEATURE].filter((f) => rest.some((c) => getFeature(c) === f));
  const rowKeys = LAYERS.map((l) => l.key).filter((k) => k !== 'frontend' && rest.some((c) => getLayer(c) === k));
  const cellOf = (lane, row) => rest.filter((c) => getFeature(c) === lane && getLayer(c) === row).sort((a, b) => a.name.localeCompare(b.name));
  const rowWidth = (n) => n * FLOW.cardW + Math.max(0, n - 1) * FLOW.cellGap;
  const rowHeight = FLOW.cardH + FLOW.rowPad * 2;

  // 1. レーン（縦の列）の位置と幅を決める
  let x = FLOW.gutter;
  const lanes = laneKeys.map((key) => {
    const w = rowWidth(Math.max(1, ...rowKeys.map((r) => cellOf(key, r).length)));
    const lane = { key, x, w, members: rest.filter((c) => getFeature(c) === key) };
    x += w + FLOW.colGap;
    return lane;
  });
  const contentRight = Math.max(x - FLOW.colGap, FLOW.gutter + rowWidth(Math.max(1, entry.length)));
  const width = contentRight + FLOW.padRight;

  const positions = {};
  const rows = [];
  let y = 0;
  // 2. 最上段: 入口（Frontend）を中央に置く
  if (entry.length > 0) {
    const left = (FLOW.gutter + contentRight) / 2 - rowWidth(entry.length) / 2;
    entry.forEach((c, i) => { positions[c.name] = { x: left + i * (FLOW.cardW + FLOW.cellGap), y: y + FLOW.rowPad }; });
    rows.push({ key: 'frontend', y, h: rowHeight, members: entry });
    y += rowHeight;
  }
  // 3. レーンの見出しの帯
  const laneHeaderY = y;
  y += FLOW.laneHeaderH;
  // 4. 各段: レーンごとのセルにカードを横に並べ、レーンの中で中央に寄せる
  rowKeys.forEach((key) => {
    lanes.forEach((lane) => {
      const list = cellOf(lane.key, key);
      const left = lane.x + (lane.w - rowWidth(list.length)) / 2;
      list.forEach((c, i) => { positions[c.name] = { x: left + i * (FLOW.cardW + FLOW.cellGap), y: y + FLOW.rowPad }; });
    });
    rows.push({ key, y, h: rowHeight, members: rest.filter((c) => getLayer(c) === key) });
    y += rowHeight;
  });

  return { width, height: y + FLOW.padBottom, lanes, rows, laneHeaderY, positions };
};

// edgePath は依存元カード a から依存先カード b への矢印の SVG パス（3 次ベジェ曲線）を返す。
// b が下の段なら a の下端 → b の上端、上の段なら a の上端 → b の下端、同じ段なら下側へ膨らませて回り込む
const edgePath = (a, b) => {
  const ax = a.x + FLOW.cardW / 2;
  const bx = b.x + FLOW.cardW / 2;
  if (b.y > a.y) {
    const y1 = a.y + FLOW.cardH;
    const y2 = b.y;
    const dy = Math.max(20, (y2 - y1) / 2);
    return `M ${ax} ${y1} C ${ax} ${y1 + dy}, ${bx} ${y2 - dy}, ${bx} ${y2}`;
  }
  if (b.y < a.y) {
    const y1 = a.y;
    const y2 = b.y + FLOW.cardH;
    const dy = Math.max(20, (y1 - y2) / 2);
    return `M ${ax} ${y1} C ${ax} ${y1 - dy}, ${bx} ${y2 + dy}, ${bx} ${y2}`;
  }
  // 同じ段: 横に離れているほど大きく膨らませる（段の間の余白からははみ出さない）
  const y = a.y + FLOW.cardH;
  const bulge = Math.min(FLOW.rowPad * 2 - 4, 12 + Math.abs(bx - ax) / 12);
  return `M ${ax} ${y} C ${ax} ${y + bulge}, ${bx} ${y + bulge}, ${bx} ${y}`;
};

// 矢印の色。base: 通常 / out: 注目中のコンテナが依存する先 / in: 注目中のコンテナに依存する元 / error: どちらかの端が異常
const EDGE_COLOR = { base: '#38bdf8', out: '#22d3ee', in: '#f59e0b', error: '#f43f5e' };

// --- 小さな部品 ---

// StatusDot は状態を発光するランプで表示する
const StatusDot = ({ status }) => (
  <span className={`inline-block w-2 h-2 rounded-full shrink-0 ${STATUS_STYLE[status].dot}`} />
);

// Readout は HUD の計器 1 つ（小さな見出し + 数値）。四隅に括弧を付ける。className で値の文字色を変えられる
const Readout = ({ label, children, className = 'text-cyan-50', title }) => (
  <div className="hud-corners px-2.5 py-1.5 bg-cyan-500/[0.05] min-w-0" style={{ '--c': 'rgba(34, 211, 238, 0.5)', '--l': '6px' }} title={title}>
    <div className="text-[11px] font-semibold tracking-wider text-cyan-400/80 uppercase">{label}</div>
    <div className={`hud-font-mono text-sm leading-tight truncate ${className}`}>{children}</div>
  </div>
);

// Metric はヘッダーに並べる指標。title はマウスオーバー時の説明、children は値の右に添える要素（グラフなど）
const Metric = ({ icon: Icon, label, value, sub, title, children }) => (
  <div className="hud-cut-sm flex items-center gap-2 pl-2.5 pr-3 py-1.5 bg-cyan-500/[0.06] border-l-2 border-cyan-400/60 whitespace-nowrap shrink-0" title={title}>
    <Icon size={15} className="text-cyan-400 shrink-0" />
    <div className="leading-none">
      <div className="text-[11px] font-semibold tracking-wider text-cyan-400/80 uppercase">{label}</div>
      <div className="flex items-baseline gap-1 mt-0.5">
        <span className="hud-font-mono text-base text-cyan-50">{value}</span>
        {sub && <span className="hud-font-mono text-[11px] text-slate-400">{sub}</span>}
      </div>
    </div>
    {children}
  </div>
);

// Sparkline は数値の推移を 60x20 の折れ線（下を塗りつぶし）で描く。縦軸は最大値（最低 1）を基準に正規化する
const Sparkline = ({ values }) => {
  const max = Math.max(1, ...values);
  const points = values.map((v, i) => `${(i / (values.length - 1)) * 60},${19 - (v / max) * 17}`).join(' ');
  return (
    <svg width="60" height="20" className="shrink-0">
      <polygon points={`0,20 ${points} 60,20`} fill="rgba(34, 211, 238, 0.12)" />
      <polyline points={points} fill="none" stroke="rgba(34, 211, 238, 0.9)" strokeWidth="1.25" />
    </svg>
  );
};

// ActivityBars はログ出力中に動くイコライザー。出力がなければ低い棒のまま止まる
const ActivityBars = ({ active }) => (
  <span className="ml-auto flex items-end gap-[2px] h-2.5 shrink-0" title="ログ出力中">
    {[0, 1, 2].map((i) => (
      <span
        key={i}
        className={`w-[2px] ${active ? 'bg-cyan-300 hud-eq' : 'bg-cyan-900'}`}
        style={active ? { animationDelay: `${i * 0.15}s` } : { height: `${3 + i * 2}px` }}
      />
    ))}
  </span>
);

// Clock は現在時刻と日付を表示する。毎秒の再描画をこの部品だけに閉じ込める
const Clock = () => {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="text-right leading-none pl-2 whitespace-nowrap shrink-0">
      <div className="hud-font-mono text-lg text-cyan-100">{pad2(now.getHours())}:{pad2(now.getMinutes())}:{pad2(now.getSeconds())}</div>
      <div className="hud-font-mono text-[11px] text-slate-400 mt-1">{now.getFullYear()}.{pad2(now.getMonth() + 1)}.{pad2(now.getDate())}</div>
    </div>
  );
};

// SystemStatus は全体の状態を 1 つのランプで示す。異常・異常終了があれば ALERT、起動待ちなどがあれば CAUTION、なければ NOMINAL
const SystemStatus = ({ counts }) => {
  const s = counts.error + counts.down > 0
    ? { label: 'ALERT', sub: '異常あり', cls: 'text-rose-300 border-rose-500/70 bg-rose-500/10', lamp: 'bg-rose-500 shadow-[0_0_10px_#f43f5e] hud-blink' }
    : counts.warn > 0
      ? { label: 'CAUTION', sub: '起動待ちあり', cls: 'text-amber-300 border-amber-500/60 bg-amber-500/10', lamp: 'bg-amber-400 shadow-[0_0_10px_#fbbf24]' }
      : { label: 'NOMINAL', sub: '全系統正常', cls: 'text-emerald-300 border-emerald-500/50 bg-emerald-500/[0.07]', lamp: 'bg-emerald-400 shadow-[0_0_10px_#34d399]' };
  return (
    <div className={`hud-cut-sm flex items-center gap-2.5 px-3 py-1.5 border whitespace-nowrap shrink-0 ${s.cls}`}>
      <span className={`w-2.5 h-2.5 rounded-full ${s.lamp}`} />
      <span className="text-sm font-bold tracking-widest">SYSTEM {s.label}</span>
      <span className="hidden 2xl:inline text-xs opacity-80">{s.sub}</span>
    </div>
  );
};

// HudToggle は HUD 風のオン / オフ切り替えボタン。title はマウスオーバー時の説明
const HudToggle = ({ checked, onChange, label, title }) => (
  <button
    onClick={() => onChange(!checked)}
    title={title}
    className={`hud-cut-sm flex items-center gap-1.5 px-2.5 py-1 text-[13px] border transition-colors whitespace-nowrap ${
      checked ? 'border-cyan-400/70 text-cyan-100 bg-cyan-500/15' : 'border-slate-600 text-slate-300 hover:border-cyan-500/50 hover:text-cyan-100'
    }`}
  >
    <span className={`w-1.5 h-1.5 ${checked ? 'bg-cyan-300 shadow-[0_0_6px_#67e8f9]' : 'bg-slate-500'}`} />
    {label}
  </button>
);

// --- フローチャート ---

// FlowNode はフローチャート上のコンテナ 1 枚。角を切った枠・左端の発光帯・状態ランプで状態を、下段に CPU とメモリを表示する。
// 状態表記（ALERT・OFFLINE など）は稼働中以外のときだけ出し、異常が目立つようにする。
// 選択中はロックオンの括弧で囲み、異常なら赤く脈動させる。dimmed は注目中のコンテナと無関係なとき（または検索に当たらないとき）に薄く表示する
const FlowNode = ({ c, pos, status, stats, isActive, isSelected, dimmed, onSelect, onHover }) => {
  const st = STATUS_STYLE[status];
  const cpu = Math.min(100, stats?.cpuPercent || 0);
  return (
    <div
      id={`node-${c.name}`}
      onClick={(e) => { e.stopPropagation(); onSelect(c.name); }}
      onMouseEnter={() => onHover(c.name)}
      onMouseLeave={() => onHover(null)}
      title={`${c.name}\n${c.image}\n${c.status}`}
      className={`hud-node absolute cursor-pointer select-none transition-opacity duration-200 ${dimmed ? 'opacity-30' : status === 'done' ? 'opacity-70' : ''} ${status === 'error' ? 'hud-alert' : ''}`}
      style={{ left: pos.x, top: pos.y, width: FLOW.cardW, height: FLOW.cardH, '--frame': isSelected ? 'rgba(103, 232, 249, 0.95)' : st.frame }}
    >
      {isSelected && <div className="hud-corners hud-lock absolute -inset-[6px] pointer-events-none" style={{ '--c': '#67e8f9', '--l': '9px' }} />}
      <div className="hud-node-frame hud-cut h-full p-px">
        <div className={`hud-cut relative h-full overflow-hidden pl-3 pr-2.5 py-2 flex flex-col justify-between ${isSelected ? 'bg-[#0a2033]' : 'bg-[#071322]'}`}>
          {/* 左端の発光帯（状態の色） */}
          <div className="absolute left-0 top-0 bottom-0 w-[3px]" style={{ background: st.color, boxShadow: `0 0 8px ${st.color}` }} />
          <div className="flex items-center gap-1.5">
            <StatusDot status={status} />
            <span className="truncate text-[15px] font-semibold leading-none text-white">{shortName(c)}</span>
            <ActivityBars active={isActive} />
          </div>
          <div className="flex items-center gap-1.5 hud-font-mono text-[11px] leading-none text-slate-300">
            {status !== 'ok' && <span className={st.text}>{st.label}</span>}
            {stats && (
              <>
                {/* CPU 使用率のミニバー（100% で満杯） */}
                <span className="flex-1 min-w-[16px] h-[3px] bg-cyan-950 relative" title={`CPU ${stats.cpuPercent.toFixed(1)}%`}>
                  <span className="absolute inset-y-0 left-0 bg-cyan-400" style={{ width: `${cpu}%` }} />
                </span>
                <span>{stats.cpuPercent.toFixed(0)}%</span>
                <span className="text-slate-400">{formatBytes(stats.memUsage)}</span>
              </>
            )}
            {c.restartCount > 0 && (
              <span className={`flex items-center gap-px text-amber-400 ${stats ? '' : 'ml-auto'}`} title="再起動回数">
                <RotateCcw size={10} />{c.restartCount}
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

// FlowCanvas は縦型フローチャート全体（段の名前・レーン・矢印・カード）を描く。
// 配置は buildLayout の座標（縮小前）で描き、全体を scale で縮小して画面幅に収める（広い画面では中央に寄せる）
const FlowCanvas = ({ layout, nodes, edges, focus, neighbors, matches, statusOf, containerStats, activeLogsMap, selectedName, showSharedEdges, scale, onSelect, onHover }) => {
  const statusByName = Object.fromEntries(nodes.map((c) => [c.name, statusOf(c)]));
  const lanesTop = layout.laneHeaderY + FLOW.laneHeaderH;
  const lanesBottom = layout.height - FLOW.padBottom;

  // 矢印を種類分けする。注目中のコンテナに関わる矢印は色付き・発光で前面に、それ以外は薄く背面に描く。
  // 共通基盤への矢印は、注目中のコンテナに関わるもの以外は「共通基盤への線」がオンのときだけ描く
  const drawn = [];
  edges.forEach((e) => {
    const a = layout.positions[e.from];
    const b = layout.positions[e.to];
    if (!a || !b) return;
    const touches = focus && (e.from === focus || e.to === focus);
    if (!touches && e.shared && !showSharedEdges) return;
    const isError = statusByName[e.from] === 'error' || statusByName[e.to] === 'error'
      || statusByName[e.from] === 'down' || statusByName[e.to] === 'down';
    const kind = touches ? (e.from === focus ? 'out' : 'in') : isError ? 'error' : 'base';
    drawn.push({
      ...e,
      id: `edge-${e.from}-${e.to}`,
      d: edgePath(a, b),
      kind,
      front: touches,
      dim: !!focus && !touches,
      // パケットを流すのは、注目中の矢印と、どちらかの端でログが出ている矢印
      packet: touches || (!focus && !!(activeLogsMap[e.from] || activeLogsMap[e.to])),
    });
  });
  drawn.sort((x, y) => Number(x.front) - Number(y.front));

  return (
    <div className="relative mx-auto" style={{ width: layout.width * scale, height: layout.height * scale }}>
      <div className="absolute top-0 left-0 origin-top-left" style={{ width: layout.width, height: layout.height, transform: `scale(${scale})` }}>
        {/* 段（レイヤー）の帯。左端に番号・段の名前・補足を出す */}
        {layout.rows.map((row, i) => {
          const l = LAYERS.find((x) => x.key === row.key);
          return (
            <div
              key={row.key}
              className="absolute left-0 border-t border-cyan-400/10"
              style={{ top: row.y, height: row.h, width: layout.width, background: i % 2 === 0 ? 'rgba(34, 211, 238, 0.03)' : 'transparent' }}
            >
              <div className="absolute left-3 top-1/2 -translate-y-1/2 leading-tight" style={{ width: FLOW.gutter - 20 }}>
                <div className="flex items-baseline gap-1.5">
                  <span className="hud-font-mono text-[11px] text-cyan-500">{pad2(i + 1)}</span>
                  <span className="text-[15px] font-bold tracking-wider uppercase text-cyan-100">{l.label}</span>
                </div>
                <div className="text-[12px] text-slate-400">{l.sub}</div>
              </div>
              {/* 段の境目に、流れの向き（下向き）を示す印を置く */}
              {i > 0 && <ChevronDown size={14} className="absolute text-cyan-500/70" style={{ left: FLOW.gutter / 2 - 7, top: -7 }} />}
            </div>
          );
        })}

        {/* レーン（機能ごとの縦の列）の薄い帯。矢印の背面に描く */}
        {layout.lanes.map((lane) => (
          <div
            key={`band-${lane.key}`}
            className="absolute border-x border-cyan-400/[0.08]"
            style={{ left: lane.x - 4, top: lanesTop, width: lane.w + 8, height: lanesBottom - lanesTop, background: 'linear-gradient(180deg, rgba(34, 211, 238, 0.05), rgba(34, 211, 238, 0.01))' }}
          />
        ))}

        {/* 矢印（depends_on）。依存元 → 依存先の向き。注目中の矢印は発光させ、矢印に沿ってパケットを流す */}
        <svg className="absolute top-0 left-0 pointer-events-none" width={layout.width} height={layout.height}>
          <defs>
            {Object.entries(EDGE_COLOR).map(([kind, color]) => (
              <marker key={kind} id={`arrow-${kind}`} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M 0 0 L 10 5 L 0 10 z" fill={color} />
              </marker>
            ))}
            {/* 範囲は座標で指定する（既定の bounding box 基準だと、幅 0 の垂直な矢印が描画されなくなる） */}
            <filter id="edge-glow" filterUnits="userSpaceOnUse" x="0" y="0" width={layout.width} height={layout.height}>
              <feGaussianBlur stdDeviation="2" result="blur" />
              <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
            </filter>
          </defs>
          {drawn.map((e) => (
            <g key={e.id}>
              <path
                id={e.id}
                d={e.d}
                fill="none"
                stroke={EDGE_COLOR[e.kind]}
                strokeWidth={e.front ? 2 : 1.3}
                opacity={e.dim ? 0.12 : e.front ? 1 : 0.55}
                markerEnd={`url(#arrow-${e.kind})`}
                filter={e.front ? 'url(#edge-glow)' : undefined}
              />
              {e.packet && !e.dim && (
                <circle r={e.front ? 2.8 : 2.2} fill={e.front ? EDGE_COLOR[e.kind] : '#a5f3fc'} filter="url(#edge-glow)">
                  <animateMotion dur={e.front ? '1.4s' : '2.2s'} repeatCount="indefinite">
                    <mpath href={`#${e.id}`} />
                  </animateMotion>
                </circle>
              )}
            </g>
          ))}
        </svg>

        {/* レーンの見出し（状態ランプ・レーン名・稼働中/全体の数）。入口からの矢印が文字に重ならないよう、矢印より前面に描く */}
        {layout.lanes.map((lane) => {
          const status = worstStatus(lane.members.map((c) => statusByName[c.name]));
          const running = lane.members.filter((c) => c.state === 'running').length;
          return (
            <div
              key={`head-${lane.key}`}
              className="absolute flex flex-col items-center justify-center bg-[#06121f] border border-cyan-400/20"
              style={{ left: lane.x - 4, top: layout.laneHeaderY + 6, width: lane.w + 8, height: FLOW.laneHeaderH - 10, borderTop: `2px solid ${STATUS_STYLE[status].color}` }}
            >
              <div className="flex items-center gap-1.5">
                <StatusDot status={status} />
                <span className="text-[14px] font-bold tracking-wider uppercase text-cyan-50">{lane.key}</span>
              </div>
              <div className="hud-font-mono text-[11px] text-slate-400" title="稼働中 / 全体">{running}/{lane.members.length} online</div>
            </div>
          );
        })}

        {/* カード。注目中のコンテナがあれば、それと直接つながるもの以外を薄くする。検索中は当たらないものを薄くする */}
        {nodes.map((c) => {
          const pos = layout.positions[c.name];
          if (!pos) return null;
          const dimmed = (focus && c.name !== focus && !neighbors.has(c.name)) || (matches && !matches.has(c.name));
          return (
            <FlowNode
              key={c.name}
              c={c}
              pos={pos}
              status={statusByName[c.name]}
              stats={containerStats[c.name]}
              isActive={!!activeLogsMap[c.name]}
              isSelected={c.name === selectedName}
              dimmed={dimmed}
              onSelect={onSelect}
              onHover={onHover}
            />
          );
        })}
      </div>
    </div>
  );
};

// --- 概要バー（フローチャートの上） ---

// SummaryBar は状態別の件数と「要確認」コンテナのチップ、表示の切り替え、検索、凡例を並べる。チップのクリックでそのコンテナを選択する
const SummaryBar = ({ counts, issues, statusOf, onSelect, query, setQuery, showCompleted, setShowCompleted, showSharedEdges, setShowSharedEdges }) => (
  <div className="relative px-5 py-2 border-b border-cyan-500/15 bg-[#030a16]/70 shrink-0 space-y-2 z-10">
    <div className="flex items-center gap-2 flex-wrap">
      {/* 状態別の件数。warn（起動中など）は「稼働中」に含める */}
      {[
        { label: '稼働中', value: counts.ok + counts.warn, status: 'ok' },
        { label: '正常終了', value: counts.done, status: 'done' },
        { label: '異常終了', value: counts.down, status: 'down' },
        { label: '異常', value: counts.error, status: 'error' },
      ].map((t) => {
        const hot = t.value > 0 && (t.status === 'down' || t.status === 'error');
        return (
          <div key={t.label} className={`hud-cut-sm flex items-center gap-2 px-2.5 py-1 border ${hot ? 'border-rose-500/60 bg-rose-500/10' : 'border-cyan-500/20 bg-cyan-500/[0.05]'}`}>
            <StatusDot status={t.status} />
            <span className="text-[13px] text-slate-300">{t.label}</span>
            <span className={`hud-font-mono text-base leading-none ${hot ? 'text-rose-300' : 'text-white'}`}>{t.value}</span>
          </div>
        );
      })}

      <div className="ml-auto flex items-center gap-2">
        {/* 凡例: 矢印の色の意味 */}
        <span className="hidden xl:flex items-center gap-3 mr-2 text-[13px] text-slate-300">
          <span className="flex items-center gap-1.5"><span className="w-4 h-px" style={{ background: EDGE_COLOR.base }} />depends_on</span>
          <span className="flex items-center gap-1.5"><span className="w-4 h-0.5" style={{ background: EDGE_COLOR.out }} />依存先</span>
          <span className="flex items-center gap-1.5"><span className="w-4 h-0.5" style={{ background: EDGE_COLOR.in }} />依存元</span>
        </span>
        <HudToggle checked={showSharedEdges} onChange={setShowSharedEdges} label="共通基盤への線" title="redis・minio・app-db など共通基盤への矢印を常に表示する（オフでも、注目中のコンテナの矢印は表示する）" />
        <HudToggle checked={showCompleted} onChange={setShowCompleted} label="正常終了も表示" title="exit 0 で終わった init 系のコンテナを表示する" />
        <div className="hud-cut-sm w-56 flex items-center gap-2 px-2.5 py-1 bg-black/40 border border-cyan-500/25 focus-within:border-cyan-400/60">
          <Search size={14} className="text-cyan-400 shrink-0" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="名前・イメージで強調" className="flex-1 min-w-0 bg-transparent outline-none text-[13px] text-cyan-50 placeholder:text-slate-500" />
          {query && <button onClick={() => setQuery('')} className="text-slate-400 hover:text-cyan-100"><X size={13} /></button>}
        </div>
      </div>
    </div>
    {/* 要確認一覧（あるときだけ）。理由はマウスオーバーで出す */}
    {issues.length > 0 && (
      <div className="flex items-center gap-2 flex-wrap text-[13px]">
        <span className="flex items-center gap-1.5 font-semibold text-amber-300"><AlertTriangle size={14} />要確認</span>
        {issues.map(({ c, reasons }) => (
          <button key={c.name} onClick={() => onSelect(c.name)} title={reasons.join('\n')} className="hud-cut-sm flex items-center gap-1.5 px-2 py-0.5 border border-amber-500/30 bg-amber-500/[0.07] hover:bg-amber-500/15 text-slate-100">
            <StatusDot status={statusOf(c)} />
            <span className="font-semibold">{serviceName(c)}</span>
            <span className="text-slate-400 max-w-[16rem] truncate">{reasons[0]}</span>
          </button>
        ))}
      </div>
    )}
  </div>
);

// --- 右ペイン: コンテナ詳細 ---

// Section は詳細ペインの小見出し（「// 英語名 日本語名」）付きのまとまり
const Section = ({ title, sub, children }) => (
  <div className="space-y-1.5">
    <div className="flex items-baseline gap-2">
      <span className="text-[12px] font-bold tracking-widest text-cyan-400">{`// ${title}`}</span>
      {sub && <span className="text-[12px] text-slate-400">{sub}</span>}
    </div>
    <div className="flex flex-wrap gap-1.5 text-[13px]">{children}</div>
  </div>
);

// DepChips は依存関係にあるコンテナをチップとして並べる。クリックでそのコンテナへ移動し、空なら「なし」を表示する
const DepChips = ({ list, statusOf, onSelect }) =>
  list.length === 0 ? <span className="text-slate-500 text-[13px]">なし</span> : list.map((d) => (
    <button key={d.name} onClick={() => onSelect(d.name)} className="hud-cut-sm flex items-center gap-1.5 px-2 py-0.5 border border-cyan-500/25 bg-cyan-500/[0.06] hover:bg-cyan-500/20 hover:border-cyan-400/60 text-slate-100">
      <StatusDot status={statusOf(d)} />{serviceName(d)}
    </button>
  ));

// DetailPane は選択中コンテナの詳細（状態・リソース・ネットワーク・依存関係）と、ログ / Storage のタブを表示する
const DetailPane = ({ c, status, stats, networks, deps, rdeps, statusOf, onSelect, onClose, activeTab, setActiveTab, canShowStorage, logs, storageProps }) => {
  const logBoxRef = useRef(null);
  // Storage タブが選ばれていても、Storage を表示できないコンテナならログタブを表示する
  const tab = activeTab === 'storage' && canShowStorage ? 'storage' : 'logs';
  const st = STATUS_STYLE[status];

  // 新しいログが来たら末尾までスクロールする
  useEffect(() => {
    const el = logBoxRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [logs, tab, c.name]);

  // メモリ使用率（%）。上限が取れない場合は 0 とする
  const memPct = stats?.memLimit ? (stats.memUsage / stats.memLimit) * 100 : 0;

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* ヘッダー: 名前・状態・リソース・ネットワーク・依存関係。
          依存関係が多いと縦に長くなるため、画面の高さの 45% を上限にして中でスクロールさせ、下のログ欄の高さを確保する */}
      <div className="px-5 py-4 border-b border-cyan-500/15 space-y-3.5 shrink-0 max-h-[45vh] overflow-y-auto">
        <div className="flex items-start gap-3">
          <div className="min-w-0">
            <div className="hud-font-mono text-[11px] tracking-widest text-cyan-500">TARGET // {LAYERS.find((l) => l.key === getLayer(c))?.label.toUpperCase()} · {getFeature(c)}</div>
            <h2 className="flex items-center gap-2.5 mt-1">
              <StatusDot status={status} />
              <span className="text-xl font-bold tracking-wide text-white truncate">{serviceName(c)}</span>
            </h2>
            <div className="hud-font-mono text-[12px] text-slate-400 truncate mt-0.5">{c.name} · {c.image}</div>
          </div>
          <button onClick={onClose} title="閉じる" className="ml-auto p-1.5 text-cyan-400 hover:text-white hover:bg-cyan-500/10"><X size={18} /></button>
        </div>
        <div className="grid grid-cols-3 gap-2">
          <Readout label="状態" className={st.text} title={c.status}>{st.label}</Readout>
          <Readout label="ヘルス" className={c.health === 'unhealthy' ? 'text-rose-400' : 'text-cyan-50'}>{c.health || '-'}</Readout>
          <Readout label="稼働時間">{c.state === 'running' && c.uptime ? c.uptime : '-'}</Readout>
          <Readout label="再起動" className={c.restartCount > 0 ? 'text-amber-300' : 'text-cyan-50'}>{c.restartCount ?? 0} 回</Readout>
          <Readout label="CPU">{stats ? `${stats.cpuPercent.toFixed(1)}%` : '-'}</Readout>
          <Readout label="メモリ">{stats ? `${formatBytes(stats.memUsage)} · ${memPct.toFixed(1)}%` : '-'}</Readout>
        </div>
        {networks.length > 0 && (
          <Section title="NETWORK" sub="ネットワーク">
            {networks.map((n) => <span key={n} className="px-2 py-0.5 border border-slate-600 bg-black/30 text-slate-200 hud-font-mono text-[12px]">{n}</span>)}
          </Section>
        )}
        {/* 依存関係は compose のラベルから求めるため、compose 管理下のコンテナでのみ表示する */}
        {c.project && (
          <>
            <Section title="DEPENDS ON" sub="依存先"><DepChips list={deps} statusOf={statusOf} onSelect={onSelect} /></Section>
            <Section title="USED BY" sub="依存元"><DepChips list={rdeps} statusOf={statusOf} onSelect={onSelect} /></Section>
          </>
        )}
      </div>

      {/* タブ切り替え。Storage タブは表示できるコンテナのときだけ出す */}
      <div className="flex items-center gap-1 px-5 pt-3 shrink-0">
        {['logs', ...(canShowStorage ? ['storage'] : [])].map((t) => (
          <button
            key={t}
            onClick={() => setActiveTab(t)}
            className={`hud-cut-sm text-[13px] font-bold tracking-wider px-3 py-1.5 border ${tab === t ? 'border-cyan-400/70 bg-cyan-500/15 text-white' : 'border-transparent text-slate-400 hover:text-cyan-100'}`}
          >
            {t === 'logs' ? 'LOG' : 'STORAGE'}
          </button>
        ))}
      </div>
      {/* タブの中身。ログは端末風に表示し、エラー行に該当する行を赤くする */}
      <div className="flex-1 min-h-0 p-5 pt-3">
        {tab === 'storage' ? <StorageExplorer {...storageProps} /> : (
          <div ref={logBoxRef} className="h-full overflow-y-auto bg-black/60 border border-cyan-500/20 p-3 hud-font-mono text-[12.5px] leading-relaxed select-text">
            {logs.length === 0 ? (
              <div className="text-slate-500">ログを待っています…</div>
            ) : logs.map((log, i) => (
              <div key={i} className={`whitespace-pre-wrap break-all ${ERROR_LINE.test(log) ? 'text-rose-300' : 'text-slate-200'}`}>
                <span className="text-cyan-700 select-none mr-2">›</span>{log}
              </div>
            ))}
            <span className="hud-blink text-cyan-300">█</span>
          </div>
        )}
      </div>
    </div>
  );
};

// StorageExplorer は MinIO のバケット・フォルダ・ファイルを一覧するファイラー。
// 行のクリックで選択、ダブルクリックでバケット / フォルダの中へ移動する。アップロードと削除はバケット内でのみ行える
const StorageExplorer = ({ loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete }) => (
  <div className="h-full bg-black/40 border border-cyan-500/20 flex flex-col overflow-hidden">
    {/* ツールバー: 戻る・パンくず（Root › バケット › プレフィックス）・アップロード / 削除 */}
    <div className="p-2 border-b border-cyan-500/15 bg-cyan-500/[0.04] flex items-center justify-between gap-2">
      <div className="flex items-center gap-2 min-w-0">
        <button onClick={handleGoBack} disabled={!currentBucket && !currentPrefix} className="p-1.5 hover:bg-cyan-500/10 disabled:opacity-30 text-cyan-300"><ArrowLeft className="w-4 h-4" /></button>
        <div className="flex items-center gap-1 bg-black/40 border border-cyan-500/15 px-3 py-1 text-[13px] text-slate-200 hud-font-mono min-w-0 overflow-hidden">
          <span className="text-cyan-300">ROOT</span>
          {currentBucket && <><ChevronRight className="w-3 h-3 text-cyan-700" /><span className="text-cyan-300">{currentBucket}</span></>}
          {currentPrefix.split('/').filter(Boolean).map((p, i) => <React.Fragment key={i}><ChevronRight className="w-3 h-3 text-cyan-700" /><span>{p}</span></React.Fragment>)}
        </div>
      </div>
      {currentBucket && (
        <div className="flex items-center gap-2 shrink-0">
          {/* ファイル選択欄は隠しておき、Upload ボタンからクリックを転送して開く */}
          <input type="file" ref={fileInputRef} onChange={handleUpload} className="hidden" />
          <button onClick={() => fileInputRef.current?.click()} className="hud-cut-sm flex items-center gap-1.5 text-[13px] border border-cyan-400/60 bg-cyan-500/15 text-cyan-50 px-3 py-1.5 hover:bg-cyan-500/25"><Upload className="w-3.5 h-3.5" /> Upload</button>
          <button onClick={handleDelete} disabled={!selectedItem || selectedItem.type !== 'file'} className="hud-cut-sm flex items-center gap-1.5 text-[13px] bg-rose-600/15 border border-rose-500/40 text-rose-300 px-3 py-1.5 disabled:opacity-30"><Trash2 className="w-3.5 h-3.5" /> Delete</button>
        </div>
      )}
    </div>
    {/* 一覧表。読み込み中・空・項目ありの 3 通りで表示を切り替える */}
    <div className="flex-1 overflow-y-auto">
      <table className="w-full text-left text-[13px]">
        <thead><tr className="border-b border-cyan-500/15 text-cyan-400 bg-[#030a16] sticky top-0 text-[12px] tracking-wider"><th className="p-2.5 pl-4 font-semibold">NAME</th><th className="p-2.5 font-semibold w-24">TYPE</th><th className="p-2.5 font-semibold w-28 text-right pr-4">SIZE</th></tr></thead>
        <tbody>
          {loading ? <tr><td colSpan="3" className="p-8 text-center text-slate-400">Loading…</td></tr> : items.length === 0 ? <tr><td colSpan="3" className="p-8 text-center text-slate-500">Empty</td></tr> : items.map((item, index) => <tr key={index} onClick={() => setSelectedItem(item)} onDoubleClick={() => handleDoubleClick(item)} className={`border-b border-cyan-500/[0.08] cursor-pointer ${selectedItem?.name === item.name ? 'bg-cyan-500/20' : 'hover:bg-cyan-500/[0.06]'}`}><td className="p-2 pl-4 flex items-center gap-2 font-medium text-slate-100">{item.type === 'bucket' ? <Database className="w-4 h-4 text-emerald-400" /> : item.type === 'folder' ? <Folder className="w-4 h-4 text-amber-400" /> : <FileText className="w-4 h-4 text-cyan-400" />}<span>{item.name}</span></td><td className="p-2 uppercase hud-font-mono text-[11px] text-slate-400">{item.type}</td><td className="p-2 pr-4 text-right hud-font-mono text-slate-400">{item.size ? `${(item.size / 1024).toFixed(1)} KB` : '-'}</td></tr>)}
        </tbody>
      </table>
    </div>
  </div>
);

// VolumesModal は Docker ボリュームの一覧（名前・ドライバー・マウントポイント）をモーダルで表示する。
// 一覧が空の間は Loading... と表示する（ボリュームが本当に 0 件の場合も同じ表示になる）
const VolumesModal = ({ onClose, volumes }) => (
  <div className="fixed inset-0 bg-black/70 backdrop-blur-sm z-30 flex items-center justify-center p-4" onClick={onClose}>
    <div className="hud-corners hud-slide-in w-full max-w-2xl bg-[#040c1a]/95 border border-cyan-500/25 shadow-[0_0_40px_rgba(34,211,238,0.12)] flex flex-col" style={{ '--l': '14px' }} onClick={(e) => e.stopPropagation()}>
      <div className="p-4 border-b border-cyan-500/15 flex items-center justify-between">
        <h1 className="flex items-center gap-2"><HardDrive className="w-5 h-5 text-cyan-400" /><span className="text-base font-bold tracking-widest text-white">VOLUMES</span></h1>
        <button onClick={onClose} className="p-2 text-cyan-400 hover:text-white"><X className="w-5 h-5" /></button>
      </div>
      <div className="p-4 overflow-y-auto max-h-[60vh]">
        {volumes.length === 0 ? <div className="text-slate-400 text-center py-6">Loading…</div> : (
          <table className="w-full text-left text-[13px] hud-font-mono">
            <thead><tr className="border-b border-cyan-500/15 text-cyan-400 text-[12px] tracking-wider"><th className="p-2">NAME</th><th className="p-2">DRIVER</th><th className="p-2 text-right">MOUNTPOINT</th></tr></thead>
            <tbody>
              {volumes.map((v, i) => (
                <tr key={i} className="border-b border-cyan-500/[0.08] hover:bg-cyan-500/[0.05]">
                  <td className="p-2 text-white">{v.name}</td>
                  <td className="p-2 text-slate-300">{v.driver}</td>
                  <td className="p-2 text-slate-400 text-right truncate max-w-xs" title={v.mountpoint}>{v.mountpoint}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  </div>
);

// --- メインのアプリコンポーネント ---
// App は監視ダッシュボード全体。バックエンド（/api, /ws）から Atomosidea のコンテナ情報・統計・ログを取得し、
// 中央に縦型のフローチャート、コンテナを選ぶと右に詳細を表示する
export default function App() {
  // バックエンドから定期取得するデータ（バックエンドが Atomosidea の compose プロジェクトに絞って返す）
  const [containers, setContainers] = useState([]);
  const [containerStats, setContainerStats] = useState({});
  const [topology, setTopology] = useState([]);
  const [volumes, setVolumes] = useState([]);
  const [showVolumes, setShowVolumes] = useState(false);
  const [activeUsers, setActiveUsers] = useState(0);
  // DoS 対策の状態（/api/guard/status。ディスクの空き・処理待ち・429 などの件数・超えているしきい値）
  const [guard, setGuard] = useState(null);

  // フローチャートと選択の UI 状態
  // hoveredName: マウスを乗せているコンテナ / showSharedEdges: 共通基盤への矢印を常に出すか
  const [query, setQuery] = useState('');
  const [showCompleted, setShowCompleted] = useState(false);
  const [showSharedEdges, setShowSharedEdges] = useState(false);
  const [hoveredName, setHoveredName] = useState(null);
  const [selectedName, setSelectedName] = useState(null);
  const [activeTab, setActiveTab] = useState('logs');

  // ログ関連の状態（いずれもコンテナ名をキーにしたマップ）
  // logsMap: 表示用のログ行 / activeLogsMap: 直近にログが出たか / errorLoopState: エラー行を受信した時刻（直近 5 件）
  // hasStorageMap: ログから MinIO と判定したか / lps, lpsHistory: 1 秒あたりのログ行数とその直近 30 回分の推移
  const [logsMap, setLogsMap] = useState({});
  const [activeLogsMap, setActiveLogsMap] = useState({});
  const [errorLoopState, setErrorLoopState] = useState({});
  const [hasStorageMap, setHasStorageMap] = useState({});
  const [lps, setLps] = useState(0);
  const [lpsHistory, setLpsHistory] = useState(new Array(30).fill(0));

  // Storage タブ（MinIO ファイラー）の状態
  const [items, setItems] = useState([]);
  const [selectedItem, setSelectedItem] = useState(null);
  const [loading, setLoading] = useState(false);
  const [currentBucket, setCurrentBucket] = useState('');
  const [currentPrefix, setCurrentPrefix] = useState('');

  // フローチャートを置く領域の幅（縮小率の計算に使う）
  const [canvasBoxWidth, setCanvasBoxWidth] = useState(0);

  // 再描画を起こさずに保持する値（WebSocket のコールバックやタイマーから読み書きする）
  // wsMapRef: コンテナ名 → ログ用 WebSocket / logBufferRef: コンテナ名 → まだ state に反映していないログ行
  // lastActivityRef: コンテナ名 → 最後にログを受信した時刻（ms） / logCountRef: 前回の LPS 集計以降に受信したログ行数
  // hasStorageRef: MinIO と判定済みのコンテナ（state 更新を 1 回に抑えるため）
  // stoppedLoadedRef: 停止中コンテナのうち、既存ログの読み込み接続を開始済みのもの
  // errorClearTimeoutsRef: コンテナ名 → エラー表示をリセットするタイマー
  // canvasBoxRef: フローチャートを置く領域
  const wsMapRef = useRef({});
  const logBufferRef = useRef({});
  const lastActivityRef = useRef({});
  const logCountRef = useRef(0);
  const hasStorageRef = useRef({});
  const stoppedLoadedRef = useRef({});
  const errorClearTimeoutsRef = useRef({});
  const fileInputRef = useRef(null);
  const canvasBoxRef = useRef(null);

  // fetchJson は url を GET し、成功したら JSON を apply に渡す。失敗時はコンソールに出すだけで、直前の表示はそのまま残す
  const fetchJson = useCallback(async (url, apply) => {
    try {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
      apply(await res.json());
    } catch (err) {
      console.error(`Fetch ${url} error:`, err);
    }
  }, []);

  // 各 API の取得関数。配列を期待するものは、配列以外が返ったら空配列にする
  const fetchContainers = useCallback(() => fetchJson('/api/containers', (d) => setContainers(Array.isArray(d) ? d : [])), [fetchJson]);
  const fetchContainerStats = useCallback(() => fetchJson('/api/containers/stats', (d) => setContainerStats(d || {})), [fetchJson]);
  const fetchActiveUsers = useCallback(() => fetchJson('/api/connections/count', (d) => setActiveUsers(d.count)), [fetchJson]);
  const fetchTopology = useCallback(() => fetchJson('/api/topology', (d) => setTopology(Array.isArray(d) ? d : [])), [fetchJson]);
  const fetchVolumes = useCallback(() => fetchJson('/api/volumes', (d) => setVolumes(Array.isArray(d) ? d : [])), [fetchJson]);
  const fetchGuard = useCallback(() => fetchJson('/api/guard/status', (d) => setGuard(d || null)), [fetchJson]);

  // 初回に全データを取得し、その後はデータごとの間隔で定期的に取り直す
  useEffect(() => {
    fetchContainers();
    fetchActiveUsers();
    fetchContainerStats();
    fetchTopology();
    fetchVolumes();
    fetchGuard();
    const containerInterval = setInterval(fetchContainers, 3000);
    const statsInterval = setInterval(fetchContainerStats, 3000);
    const userInterval = setInterval(fetchActiveUsers, 10000);
    // コンテナの追加・削除やネットワーク・ボリュームの変化を反映するため、トポロジーとボリュームも定期的に取り直す
    const topologyInterval = setInterval(fetchTopology, 15000);
    const volumeInterval = setInterval(fetchVolumes, 15000);
    // バックエンドは 1 分ごとに集めるので、それより少し短い間隔で読む
    const guardInterval = setInterval(fetchGuard, 30000);
    return () => {
      clearInterval(containerInterval);
      clearInterval(statsInterval);
      clearInterval(userInterval);
      clearInterval(topologyInterval);
      clearInterval(volumeInterval);
      clearInterval(guardInterval);
    };
  }, [fetchContainers, fetchActiveUsers, fetchContainerStats, fetchTopology, fetchVolumes, fetchGuard]);

  // フローチャート領域の幅を監視する（ウィンドウのリサイズや詳細ペインの開閉で変わる）
  useEffect(() => {
    const el = canvasBoxRef.current;
    if (!el) return undefined;
    const ro = new ResizeObserver(([entry]) => setCanvasBoxWidth(entry.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // コンテナを選んだら、そのカードが見える位置までフローチャートをスクロールする（詳細ペインが開いて幅が変わるのを待つ）
  useEffect(() => {
    if (!selectedName) return undefined;
    const t = setTimeout(() => {
      document.getElementById(`node-${selectedName}`)?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'nearest' });
    }, 250);
    return () => clearTimeout(t);
  }, [selectedName]);

  // 表示上のプロジェクト名（バックエンドが絞り込んだプロジェクト）
  const project = useMemo(() => containers.find((c) => c.project)?.project ?? null, [containers]);

  // LPS（2 秒ごとの集計）。2 秒間の受信行数を 2 で割って 1 秒あたりにし、推移の配列を 1 つずらして追加する
  useEffect(() => {
    const lpsInterval = setInterval(() => {
      const v = logCountRef.current / 2;
      logCountRef.current = 0;
      setLps(v);
      setLpsHistory((prev) => [...prev.slice(1), v]);
    }, 2000);
    return () => clearInterval(lpsInterval);
  }, []);

  // 受信したログは一旦バッファに溜め、300ms ごとにまとめて state に反映する（1 行ごとの再描画を避ける）
  useEffect(() => {
    const flush = setInterval(() => {
      // 1. バッファのログを logsMap に追記し、MAX_LOG_LINES を超えた古い行を捨てる
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
      // 2. 直近 800ms 以内にログが出たコンテナを「ログ出力中」とする。
      //    中身が前回と同じなら prev をそのまま返し、無駄な再描画を避ける
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

  // ログの WebSocket: 稼働中のコンテナは常時接続する。
  // 停止中のコンテナは選択時に一度だけ接続して既存のログを読む（読み終わるとサーバー側が閉じる）
  useEffect(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const isLive = (c) => c.state === 'running' || c.state === 'starting' || c.state === 'restarting';
    const wanted = new Set();

    // 1. 接続すべきコンテナを決め、まだ接続が無ければ WebSocket を開く
    containers.forEach((c) => {
      if (isSystemContainer(c.name)) return;
      const live = isLive(c);
      // 再び稼働し始めたら、停止中に読み込み済みの印を消す（次に停止したとき改めて読み込めるようにする）
      if (live) delete stoppedLoadedRef.current[c.name];
      const want = live || (c.name === selectedName && !stoppedLoadedRef.current[c.name]);
      if (!want) return;
      wanted.add(c.name);
      // 接続済み・接続中なら二重に開かない
      const existing = wsMapRef.current[c.name];
      if (existing && (existing.readyState === WebSocket.OPEN || existing.readyState === WebSocket.CONNECTING)) return;
      // 停止中のコンテナは一度だけ読む。印を付けておき、接続が閉じた後に再接続を繰り返さないようにする
      if (!live) stoppedLoadedRef.current[c.name] = true;

      const socket = new WebSocket(`${protocol}//${window.location.host}/ws/logs?container=${encodeURIComponent(c.name)}`);
      // 接続した時刻。ここから BACKLOG_MS の間に届いた行は過去ログとして扱う
      let openedAt = Infinity;
      socket.onopen = () => { openedAt = Date.now(); };
      // 1 メッセージ = ログ 1 行として、バッファ追加・MinIO 判定を行い、過去ログでなければ集計とエラー検出も行う
      socket.onmessage = (event) => {
        const logData = event.data;
        const now = Date.now();
        (logBufferRef.current[c.name] ||= []).push(logData);

        // ログに 'min.io' が出たコンテナは MinIO とみなし、Storage タブを使えるようにする
        if (!hasStorageRef.current[c.name] && logData.includes('min.io')) {
          hasStorageRef.current[c.name] = true;
          setHasStorageMap((prev) => ({ ...prev, [c.name]: true }));
        }
        // 接続直後にまとめて届く過去ログは、LPS・ログ出力中の印・エラー検出に数えない
        if (now - openedAt < BACKLOG_MS) return;
        logCountRef.current += 1;
        lastActivityRef.current[c.name] = now;
        // エラー行の受信時刻を直近 5 件だけ残す（エラー多発の判定は errorLogsMap で行う）
        if (ERROR_LINE.test(logData)) {
          setErrorLoopState((prev) => ({ ...prev, [c.name]: [...(prev[c.name] || []), now].slice(-5) }));
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
  }, [containers, selectedName]);

  // errorLogsMap は「エラー多発」と判定したコンテナのマップ。直近 5 件のエラーが 10 秒以内に収まっていれば多発とする。
  // 判定は errorLoopState が変わったときだけ行うため、時間の経過だけでは解除されない（解除は下の effect のタイマーで行う）
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
  // 停止中のコンテナ（選択時に過去ログを読んだもの）も同じように解除する。タイマーが残っている間は二重に仕掛けない
  useEffect(() => {
    Object.keys(errorLogsMap).forEach((name) => {
      if (errorClearTimeoutsRef.current[name]) return;
      errorClearTimeoutsRef.current[name] = setTimeout(() => {
        delete errorClearTimeoutsRef.current[name];
        setErrorLoopState((prev) => {
          const newState = { ...prev };
          delete newState[name];
          return newState;
        });
      }, ERROR_HIGHLIGHT_MS);
    });
  }, [errorLogsMap]);

  // アンマウント時に、残っているタイマーと WebSocket をすべて片付ける。
  // ref の中身のオブジェクトは差し替えずに中身だけ書き換えているため、マウント時に取った参照で最新の内容を片付けられる
  useEffect(() => {
    const timeouts = errorClearTimeoutsRef.current;
    const sockets = wsMapRef.current;
    return () => {
      Object.values(timeouts).forEach((t) => clearTimeout(t));
      Object.values(sockets).forEach((ws) => ws.close());
    };
  }, []);

  // statusOf はエラー多発の判定を加味したコンテナの状態を返す
  const statusOf = useCallback((c) => getStatus(c, !!errorLogsMap[c.name]), [errorLogsMap]);

  // --- フローチャート ---

  // 監視対象のコンテナ（集計用）と、フローチャートに描くコンテナ（正常終了を隠す設定なら除く）
  const monitored = useMemo(() => containers.filter((c) => !isSystemContainer(c.name)), [containers]);
  const nodes = useMemo(() => monitored.filter((c) => showCompleted || !isCompleted(c)), [monitored, showCompleted]);
  const layout = useMemo(() => buildLayout(nodes), [nodes]);

  // 矢印（compose の depends_on）。依存元 → 依存先。shared は依存先が共通基盤（redis・minio・app-db・init 系）のもの
  const edges = useMemo(() => {
    const byService = Object.fromEntries(nodes.map((c) => [c.service, c]));
    const list = [];
    nodes.forEach((a) => (a.dependsOn || []).forEach((s) => {
      const b = byService[s];
      if (b && b !== a) list.push({ from: a.name, to: b.name, shared: getFeature(b) === SHARED_FEATURE && getLayer(b) !== 'frontend' });
    }));
    return list;
  }, [nodes]);

  // 注目中のコンテナ（マウスを乗せているもの、無ければ選択中のもの）と、それと矢印で直接つながるコンテナ
  const focus = hoveredName ?? selectedName;
  const neighbors = useMemo(() => {
    const set = new Set();
    if (!focus) return set;
    edges.forEach((e) => {
      if (e.from === focus) set.add(e.to);
      if (e.to === focus) set.add(e.from);
    });
    return set;
  }, [edges, focus]);

  // 検索に当たるコンテナ（名前とイメージの部分一致）。検索していなければ null（何も薄くしない）
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return null;
    return new Set(nodes.filter((c) => c.name.toLowerCase().includes(q) || (c.image || '').toLowerCase().includes(q)).map((c) => c.name));
  }, [nodes, query]);

  // 縮小率。領域に収まるよう縮め（拡大はしない）、MIN_SCALE より小さくなるなら横スクロールにする
  const scale = canvasBoxWidth > 0 ? Math.min(1, Math.max(MIN_SCALE, (canvasBoxWidth - 16) / layout.width)) : 1;

  // --- 選択中のコンテナ ---
  const selected = useMemo(() => containers.find((c) => c.name === selectedName) || null, [containers, selectedName]);

  // コンテナ名 → 接続しているネットワーク名の配列（/api/topology の結果から作る）
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

  // checkHasStorage は Storage タブを出すコンテナかどうかを返す。
  // ログから MinIO と判定済みか、名前かイメージに 'minio' / 'storage' を含めば対象とする
  const checkHasStorage = useCallback((container) => !!container && (
    hasStorageMap[container.name]
    || ['minio', 'storage'].some((kw) => (container.name || '').toLowerCase().includes(kw) || (container.image || '').toLowerCase().includes(kw))
  ), [hasStorageMap]);
  const selectedHasStorage = checkHasStorage(selected);

  // handleSelect はコンテナを選択する。Storage を表示できるコンテナなら Storage タブ、それ以外はログタブで開き、
  // ファイラーの位置は Root に戻す
  const handleSelect = (name) => {
    setSelectedName(name);
    setActiveTab(checkHasStorage(containers.find((x) => x.name === name)) ? 'storage' : 'logs');
    setCurrentBucket('');
    setCurrentPrefix('');
  };

  // --- MinIO（Storage タブ） ---
  // fetchItems は選択中コンテナの MinIO から、現在のバケット・プレフィックス直下の一覧を取得する（バケット未選択ならバケット一覧）。
  // 取得のたびに選択中の項目は解除し、失敗時は空の一覧にする
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

  // Storage タブを開いているときは、コンテナ・バケット・プレフィックスが変わるたびに一覧を取り直す
  useEffect(() => {
    if (selectedHasStorage && activeTab === 'storage') fetchItems();
  }, [selectedHasStorage, activeTab, fetchItems]);

  // handleDoubleClick はバケットならその中（Root 直下）へ、フォルダならプレフィックスに名前を足して中へ移動する。ファイルは何もしない
  const handleDoubleClick = (item) => {
    if (item.type === 'bucket') { setCurrentBucket(item.name); setCurrentPrefix(''); }
    else if (item.type === 'folder') setCurrentPrefix((prev) => `${prev}${item.name}/`);
  };
  // handleGoBack は 1 階層上へ戻る。プレフィックスがあれば末尾のフォルダを 1 つ外し、なければバケットから Root へ戻る
  const handleGoBack = () => {
    if (currentPrefix) {
      const parts = currentPrefix.split('/').filter(Boolean);
      parts.pop();
      setCurrentPrefix(parts.length > 0 ? parts.join('/') + '/' : '');
    } else if (currentBucket) setCurrentBucket('');
  };
  // handleUpload は選んだファイルを現在のバケット・プレフィックスへアップロードし、成功したら一覧を取り直す
  const handleUpload = async (e) => {
    const file = e.target.files[0];
    if (!file || !currentBucket) return;
    const formData = new FormData();
    formData.append('file', file);
    const params = new URLSearchParams({ bucket: currentBucket, prefix: currentPrefix });
    const res = await fetch(`/api/minio/upload/${selectedName}?${params.toString()}`, { method: 'POST', body: formData });
    if (res.ok) fetchItems(); else alert('Upload failed');
  };
  // handleDelete は選択中のファイルを確認ダイアログの後に削除し、成功したら一覧を取り直す（フォルダ・バケットは削除しない）
  const handleDelete = async () => {
    if (!selectedItem || selectedItem.type !== 'file') return;
    if (!confirm(`Delete "${selectedItem.name}"?`)) return;
    const params = new URLSearchParams({ bucket: currentBucket, key: currentPrefix + selectedItem.name });
    const res = await fetch(`/api/minio/delete/${selectedName}?${params.toString()}`, { method: 'DELETE' });
    if (res.ok) fetchItems(); else alert('Delete failed');
  };
  // StorageExplorer にまとめて渡す props
  const storageProps = { loading, items, selectedItem, setSelectedItem, handleDoubleClick, handleGoBack, currentBucket, currentPrefix, fileInputRef, handleUpload, handleDelete };

  // --- 概要 ---

  // 状態別の件数と「要確認」一覧（理由付き）を求める。一覧は深刻な順、同じ深刻度なら名前順に並べる
  const { counts, issues } = useMemo(() => {
    const cnt = { error: 0, down: 0, warn: 0, ok: 0, done: 0 };
    const list = [];
    monitored.forEach((c) => {
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
  }, [monitored, statusOf, errorLogsMap]);

  // CPU は docker stats と同じく 1 コア = 100%。メモリの分母は、制限なしのコンテナが報告するホストのメモリ量（limit の最大値）
  const load = useMemo(() => {
    let cpu = 0;
    let mem = 0;
    let host = 0;
    monitored.forEach((c) => {
      const s = containerStats[c.name];
      if (!s) return;
      cpu += s.cpuPercent || 0;
      mem += s.memUsage || 0;
      host = Math.max(host, s.memLimit || 0);
    });
    return { cpu, mem, host };
  }, [monitored, containerStats]);

  return (
    <div className="hud-bg relative h-screen flex flex-col text-slate-200">
      {/* ヘッダー: ロゴ・全体の状態・指標（LPS・ユーザー数・CPU・メモリ）・Volumes・時計。下端に発光するライン */}
      <header className="relative flex items-center gap-4 px-5 h-16 shrink-0 bg-[#030a16]/85 z-10">
        <div className="absolute bottom-0 left-0 right-0 h-px" style={{ background: 'linear-gradient(90deg, transparent, rgba(34, 211, 238, 0.7) 15%, rgba(34, 211, 238, 0.7) 85%, transparent)' }} />
        {/* ロゴ: 回転する破線のリングの中にアイコン */}
        <div className="relative w-10 h-10 shrink-0">
          <svg viewBox="0 0 40 40" className="absolute inset-0 hud-spin-slow">
            <circle cx="20" cy="20" r="18" fill="none" stroke="rgba(34, 211, 238, 0.6)" strokeWidth="1" strokeDasharray="10 4 2 4" />
          </svg>
          <svg viewBox="0 0 40 40" className="absolute inset-0">
            <circle cx="20" cy="20" r="14" fill="rgba(34, 211, 238, 0.08)" stroke="rgba(34, 211, 238, 0.25)" strokeWidth="1" />
          </svg>
          <Boxes className="absolute inset-0 m-auto text-cyan-300 drop-shadow-[0_0_6px_#22d3ee]" size={18} />
        </div>
        <div className="leading-tight whitespace-nowrap shrink-0">
          <h1 className="hud-font-display text-lg font-bold tracking-[0.2em] text-cyan-50 hud-glow">ATOMOSIDEA</h1>
          <div className="text-[12px] tracking-wider text-slate-400">Operations Console<span className="hidden 2xl:inline">{` · ${project || 'no signal'}`}</span></div>
        </div>
        <SystemStatus counts={counts} />
        <div className="ml-auto flex items-center gap-2">
          <Metric icon={Activity} label="Log/s" value={lps.toFixed(1)} title="1 秒あたりのログ行数（全コンテナ合計）"><Sparkline values={lpsHistory} /></Metric>
          <Metric icon={Users} label="Users" value={activeUsers} sub="5min" title="atmosidea-frontend のログに出た IP の数（推定）" />
          <Metric icon={Cpu} label="CPU" value={`${load.cpu.toFixed(0)}%`} title="コンテナ合計（1 コア = 100%）" />
          <Metric icon={HardDrive} label="Memory" value={formatBytes(load.mem)} sub={load.host ? `/ ${formatBytes(load.host)}` : ''} title="コンテナ合計 / ホストのメモリ" />
          {guard && (
            <>
              <Metric icon={HardDrive} label="Disk" value={formatBytes(guard.disk_free)} sub={guard.disk_total ? `/ ${formatBytes(guard.disk_total)}` : ''} title="ディスクの空き / 全体（Docker のデータ領域）。空きが少ないとアップロードを断る" />
              <Metric icon={ListOrdered} label="Queue" value={`${guard.scan_backlog < 0 ? '-' : guard.scan_backlog} / ${guard.transcode_backlog < 0 ? '-' : guard.transcode_backlog}`} sub="検査/変換" title="検査待ち / 動画の変換待ち（処理中を含む）。多いとアップロードを断る" />
              <Metric icon={ShieldAlert} label="429" value={guard.last_5m?.['429'] ?? 0} sub={`5min · 1h ${guard.last_1h?.['429'] ?? 0}`} title={`回数制限で断った件数（nginx のログ）。5xx: 5min ${guard.last_5m?.['5xx'] ?? 0} / 1h ${guard.last_1h?.['5xx'] ?? 0}`} />
            </>
          )}
          <button onClick={() => setShowVolumes(true)} className="hud-cut-sm text-[13px] font-bold tracking-wider px-3 py-2.5 border border-cyan-500/35 text-cyan-200 hover:border-cyan-300 hover:bg-cyan-500/10 flex items-center gap-1.5 shrink-0">
            <HardDrive size={14} />VOLUMES
          </button>
          <Clock />
        </div>
      </header>

      {/* DoS 対策の警告（ディスク・処理待ち・429 / 5xx の件数がしきい値を超えているとき） */}
      {guard?.alerts?.length > 0 && (
        <div className="mx-4 mt-2 px-3 py-2 border border-rose-500/60 bg-rose-500/10 text-rose-200 text-sm flex flex-wrap items-center gap-x-4 gap-y-1">
          <span className="flex items-center gap-1.5 font-bold tracking-wider"><ShieldAlert size={15} />GUARD</span>
          {guard.alerts.map((a) => <span key={a}>{a}</span>)}
        </div>
      )}

      {/* 概要バー: 件数・要確認・表示の切り替え・検索 */}
      <SummaryBar
        counts={counts}
        issues={issues}
        statusOf={statusOf}
        onSelect={handleSelect}
        query={query}
        setQuery={setQuery}
        showCompleted={showCompleted}
        setShowCompleted={setShowCompleted}
        showSharedEdges={showSharedEdges}
        setShowSharedEdges={setShowSharedEdges}
      />

      <div className="flex-1 flex min-h-0">
        {/* 中央: フローチャート。上から下へスキャン光が通り過ぎる。背景をクリックすると選択を解除する */}
        <main ref={canvasBoxRef} className="hud-sweep relative flex-1 min-w-0 overflow-auto p-2" onClick={() => setSelectedName(null)}>
          {nodes.length === 0 ? (
            <div className="p-6 text-sm text-slate-400">Atomosidea のコンテナが見つかりません（メインのスタックが起動しているか確認してください）。</div>
          ) : (
            <FlowCanvas
              layout={layout}
              nodes={nodes}
              edges={edges}
              focus={focus}
              neighbors={neighbors}
              matches={matches}
              statusOf={statusOf}
              containerStats={containerStats}
              activeLogsMap={activeLogsMap}
              selectedName={selectedName}
              showSharedEdges={showSharedEdges}
              scale={scale}
              onSelect={handleSelect}
              onHover={setHoveredName}
            />
          )}
        </main>

        {/* 右ペイン: コンテナを選択中のときだけ詳細を出す（右から滑り込む） */}
        {selected && (
          <aside key={selected.name} className="hud-slide-in relative w-[480px] shrink-0 border-l border-cyan-500/25 bg-[#030a16]/95 flex flex-col min-h-0 shadow-[-12px_0_30px_rgba(0,0,0,0.5)]">
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
          </aside>
        )}
      </div>

      {showVolumes && <VolumesModal onClose={() => setShowVolumes(false)} volumes={volumes} />}

      {/* 画面の周辺減光 */}
      <div className="hud-overlay fixed inset-0 z-40" />
    </div>
  );
}
