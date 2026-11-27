import { useState, useEffect, useRef } from 'react';
import { useParams, useNavigate, Link as RouterLink, useLocation } from 'react-router-dom';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import {
  Container,
  Typography,
  Box,
  CircularProgress,
  Alert,
  Button,
  IconButton,
  Tooltip,
  FormControl,
  Select,
  MenuItem,
  SelectChangeEvent,
  Slider,
  styled,
} from '@mui/material';
import EditIcon from '@mui/icons-material/Edit';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import PauseIcon from '@mui/icons-material/Pause';
import VolumeUpIcon from '@mui/icons-material/VolumeUp';
import VolumeOffIcon from '@mui/icons-material/VolumeOff';
import FullscreenIcon from '@mui/icons-material/Fullscreen';
import FullscreenExitIcon from '@mui/icons-material/FullscreenExit';
import Hls from 'hls.js';

// --- 独自プレイヤーコントロール用のスタイル付きコンポーネント ---
// 動画の下端に重ねて表示する半透明のコントロールバー。opacity で表示/非表示を切り替える。
const ControlBar = styled(Box)(({ theme }) => ({
  position: 'absolute',
  bottom: 0,
  left: 0,
  right: 0,
  display: 'flex',
  alignItems: 'center',
  padding: theme.spacing(1, 2),
  background: 'rgba(0, 0, 0, 0.6)',
  transition: 'opacity 0.3s',
}));

// 再生位置・総時間を表示する白文字のテキスト
const TimeText = styled(Typography)({
  color: 'white',
  minWidth: '50px',
  textAlign: 'center',
});

/** GET /api/videos/:id が返す動画情報（この画面で使う項目のみ）。 */
interface Video {
  id: string;
  title: string;
  description: string;
  filename: string;
  uploader_id: string;
  created_at: string;
  /** 処理状態（scanning / processing / public / error など） */
  status: string;
  /** 処理中の進捗説明や失敗理由 */
  processing_details: string | null;
}

/** GET /api/profile/:id が返す投稿者情報（この画面では名前だけを使う）。 */
interface Uploader {
  username: string;
}

// これ以上変化しない状態（ポーリングを止める）。'failed' / 'invalid' はスキャン結果がそのまま入る。
const TERMINAL_STATUSES = ['public', 'error', 'failed', 'invalid', 'rejected', 'quarantined'];

// 秒数を「MM:SS」形式の文字列にする（60分以上は分の桁が増える）
const formatTime = (timeInSeconds: number) => {
  const minutes = Math.floor(timeInSeconds / 60);
  const seconds = Math.floor(timeInSeconds % 60);
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
};

/**
 * 動画詳細ページ。
 * 動画情報を取得し、処理が終わるまで 5 秒ごとにポーリングする。公開状態になったら
 * HLS.js で再生し、独自のコントロールバー（再生/シーク/音量/画質/フルスクリーン）を表示する。
 * 投稿者本人または管理者には編集・削除ボタンを表示する。
 */
const VideoDetailPage = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  // 遷移元が router の state で渡した初期ステータス（無ければ 'unknown'）
  const initialStatus = (location.state as { initialStatus?: string })?.initialStatus || 'unknown';

  const [video, setVideo] = useState<Video | null>(null);
  const [uploader, setUploader] = useState<Uploader | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [currentStatus, setCurrentStatus] = useState<string>(initialStatus);
  const { user, token } = useAuth();
  const navigate = useNavigate();

  // フルスクリーン対象のコンテナ、video 要素、HLS インスタンスへの参照
  const videoContainerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);

  // プレイヤーの状態
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [volume, setVolume] = useState(1);
  const [isMuted, setIsMuted] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [controlsVisible, setControlsVisible] = useState(true);

  // HLS の画質選択の状態（level -1 は「自動」を表す）
  const [availableQualities, setAvailableQualities] = useState<{ level: number, label: string }[]>([]);
  const [currentQuality, setCurrentQuality] = useState<number | 'auto'>('auto');
  const [activeQualityLabel, setActiveQualityLabel] = useState('');

  // --- [1] 動画と投稿者情報の取得 ---
  useEffect(() => {
    // ID が未確定の場合は API リクエストを発行しない (404防止)
    if (!id) return;

    let pollingInterval: number | null = null;

    // ポーリング用のタイマーを止める（二重に呼んでも安全）
    const stopPolling = () => {
      if (pollingInterval) {
        clearInterval(pollingInterval);
        pollingInterval = null;
      }
    };

    // 動画情報と投稿者情報を取得し、終端ステータスならポーリングを止める
    const fetchDetails = async () => {
      try {
        // 1. 動画情報を取得する
        const videoApiUrl = `/api/videos/${id}`;
        const videoRes = await axios.get(videoApiUrl);

        setVideo(videoRes.data);
        setCurrentStatus(videoRes.data.status);

        // 2. 終端ステータスならローディングを解除してポーリングを止め、それ以外は待機表示を続ける
        if (TERMINAL_STATUSES.includes(videoRes.data.status)) {
          setLoading(false);
          stopPolling();
        } else {
          setLoading(true);
        }

        // 3. 投稿者のプロフィールを取得する（失敗しても動画の表示は続ける）
        const uploaderId = videoRes.data?.uploader_id;
        if (uploaderId != null) {
          try {
            const profileApiUrl = `/api/profile/${uploaderId}`;
            const uploaderRes = await axios.get(profileApiUrl);
            setUploader(uploaderRes.data);
          } catch (e) {
            console.error('Failed to fetch uploader details:', e);
          }
        }

      } catch (err) {
        console.error('Failed to fetch video details:', err);
        // 非公開・チーム限定で閲覧権限が無い動画も 404 になる
        if (axios.isAxiosError(err) && err.response?.status === 404) {
          setError('動画が見つからないか、閲覧する権限がありません。');
        } else {
          setError('詳細の読み込みに失敗しました。');
        }
        setLoading(false);
        stopPolling();
      }
    };

    // 初回取得を行い、続けて 5 秒間隔のポーリングを開始する。
    // 注: この中の currentStatus は effect 実行時の値（初期ステータス）のまま更新されないため、
    // 実際の停止は fetchDetails 内の stopPolling に任せている。
    fetchDetails();

    pollingInterval = window.setInterval(() => {
      if (!TERMINAL_STATUSES.includes(currentStatus)) {
        fetchDetails();
      }
    }, 5000);

    // アンマウント時・ID 変更時にタイマーを止める
    return () => {
      stopPolling();
    };
    // ポーリングは動画 ID ごとに 1 回だけ開始する（終端ステータスでは fetchDetails 側で停止する）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  // --- [2] HLS.js のセットアップ ---
  useEffect(() => {
    // 公開済みで video 要素が描画されてからでないと再生準備ができない
    if (loading || !video || video.status !== 'public' || !videoRef.current) return;

    // 💡 すでに HLS インスタンスが存在する場合は再初期化しない（再レンダリング対策）
    if (hlsRef.current) return;

    const videoEl = videoRef.current;
    const streamUrl = `/api/videos/${video.id}/stream/playlist.m3u8`;

    // MSE が使えるブラウザでは HLS.js で再生する
    if (Hls.isSupported()) {
      const hls = new Hls({ debug: true });
      hlsRef.current = hls;

      // マニフェスト解析後、利用可能な画質の一覧を作る（先頭に「自動」を加える）
      hls.on(Hls.Events.MANIFEST_PARSED, (_event, data) => {
        const qualities = data.levels.map((level, index) => ({
          level: index,
          label: level.height ? `${level.height}p` : `${Math.round(level.bitrate / 1000)}kbps`,
        }));
        setAvailableQualities([{ level: -1, label: '自動' }, ...qualities]);
        setCurrentQuality('auto');
      });

      // 画質が切り替わったら、「自動」の横に出す実際の画質ラベルを更新する
      hls.on(Hls.Events.LEVEL_SWITCHED, (_event, data) => {
        const currentLevel = hls.levels[data.level];
        if (currentLevel) {
          const label = currentLevel.height ? `${currentLevel.height}p` : `${Math.round(currentLevel.bitrate / 1000)}kbps`;
          setActiveQualityLabel(`(${label})`);
        }
      });

      // 致命的エラー時の復旧: ネットワークエラーは読み込みを再開し、メディアエラーは復旧を試み、
      // それ以外はインスタンスを破棄する
      hls.on(Hls.Events.ERROR, (_event, data) => {
        if (data.fatal) {
          switch (data.type) {
            case Hls.ErrorTypes.NETWORK_ERROR: hls.startLoad(); break;
            case Hls.ErrorTypes.MEDIA_ERROR: hls.recoverMediaError(); break;
            default:
              hls.destroy();
              hlsRef.current = null;
              break;
          }
        }
      });

      hls.loadSource(streamUrl);
      hls.attachMedia(videoEl);

      // アンマウント時などに HLS インスタンスを破棄する
      return () => {
        if (hlsRef.current) {
          hlsRef.current.destroy();
          hlsRef.current = null;
        }
      };
    } else if (videoEl.canPlayType('application/vnd.apple.mpegurl')) {
      // Safari など HLS をネイティブ再生できるブラウザでは src に直接指定する（画質選択は出ない）
      videoEl.src = streamUrl;
    } else {
      setError('お使いのブラウザはHLS動画の再生に対応していません。');
    }
    // ポーリングで video オブジェクトが更新されるたびに HLS を作り直さないよう、id / status のみを依存にする
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loading, video?.id, video?.status]);

  // --- [3] フルスクリーンハンドリング ---
  // Esc キーなどボタン以外で解除された場合も含め、フルスクリーン状態を document と同期する
  useEffect(() => {
    const handleFullscreenChange = () => {
      setIsFullscreen(!!document.fullscreenElement);
    };
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    return () => document.removeEventListener('fullscreenchange', handleFullscreenChange);
  }, []);

  // --- プレイヤー操作のハンドラ ---
  // 再生/一時停止を切り替える（video の onPlay / onPause でも状態は同期される）
  const togglePlayPause = () => {
    if (videoRef.current) {
      isPlaying ? videoRef.current.pause() : videoRef.current.play();
      setIsPlaying(!isPlaying);
    }
  };

  // シークバーの操作で再生位置を移動する
  const handleSeek = (_event: Event, newValue: number | number[]) => {
    if (videoRef.current) {
      videoRef.current.currentTime = newValue as number;
      setCurrentTime(newValue as number);
    }
  };

  // 音量スライダーの操作を反映する。0 にしたときはミュート扱いにする
  const handleVolumeChange = (_event: Event, newValue: number | number[]) => {
    if (videoRef.current) {
      const newVolume = newValue as number;
      videoRef.current.volume = newVolume;
      setVolume(newVolume);
      setIsMuted(newVolume === 0);
    }
  };

  // ミュートを切り替える。音量 0 のまま解除した場合は 0.5 に戻す。
  // 注: ここでは isMuted の state を更新していない
  const toggleMute = () => {
    if (videoRef.current) {
      const newMuted = !isMuted;
      videoRef.current.muted = newMuted;
      if (!newMuted && volume === 0) {
        setVolume(0.5);
        videoRef.current.volume = 0.5;
      }
    }
  };

  // コンテナ（動画とコントロールバー）をフルスクリーン表示にする／解除する
  const toggleFullscreen = () => {
    if (!videoContainerRef.current) return;
    if (!isFullscreen) {
      videoContainerRef.current.requestFullscreen();
    } else {
      document.exitFullscreen();
    }
  };

  // 画質を選択する。'auto' は HLS.js の自動切り替え（currentLevel = -1）に対応する
  const handleQualityChange = (event: SelectChangeEvent<number | 'auto'>) => {
    const selectedQuality = event.target.value as number | 'auto';
    setCurrentQuality(selectedQuality);
    if (hlsRef.current) {
      hlsRef.current.currentLevel = selectedQuality === 'auto' ? -1 : (selectedQuality as number);
    }
  };

  // 確認ダイアログの後に動画を削除し、トップページへ戻る。
  // /api/videos/delete/:id は nginx で video-upload-api の DELETE /api/videos/:id に書き換えられる
  const handleDelete = async () => {
    if (window.confirm('本当にこの動画を削除しますか？')) {
      try {
        await axios.delete(`/api/videos/delete/${id}`, { headers: { Authorization: `Bearer ${token}` } });
        alert('動画を削除しました。');
        navigate('/');
      } catch (err) {
        setError('動画の削除に失敗しました。');
      }
    }
  };

  // 読み込み中、またはスキャン・変換処理中は待機画面を表示する
  if (loading || (video && (video.status === 'scanning' || video.status === 'processing'))) {
    let message = '動画を準備中...';
    if (video?.status === 'scanning') {
      message = 'セキュリティスキャンを実行中...';
    } else if (video?.status === 'processing') {
      message = video?.processing_details || '動画ファイルを準備中...';
    }
    return (
        <Container sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', mt: 4 }}>
          <CircularProgress />
          <Typography variant="h6" sx={{ mt: 2 }}>{message}</Typography>
          <Typography color="text.secondary">しばらくお待ちください...</Typography>
        </Container>
    );
  }

  // エラー・未取得・処理失敗（public 以外の終端ステータス）の表示
  if (error) return <Container><Alert severity="error">{error}</Alert></Container>;
  if (!video) return <Container><Alert severity="warning">動画が見つかりません。</Alert></Container>;
  if (video.status !== 'public' && TERMINAL_STATUSES.includes(video.status)) {
    return <Container><Alert severity="error">動画の処理に失敗しました: {video.processing_details || video.status}</Alert></Container>;
  }

  // 投稿者本人または管理者なら編集・削除ボタンを出す（表示制御のみ。実際の権限判定はバックエンドで行う）
  const canEdit = user && (String(user.userID) === String(video.uploader_id) || user.isAdmin);

  return (
      <Container>
        <Typography variant="h4" component="h1" gutterBottom>{video.title}</Typography>

        {/* プレイヤー本体。マウスが乗っている間だけコントロールバーを表示する */}
        <Box
            ref={videoContainerRef}
            sx={{ width: '800px', height: '450px', maxWidth: '100%', mx: 'auto', mb: 3, backgroundColor: 'black', position: 'relative' }}
            onMouseEnter={() => setControlsVisible(true)}
            onMouseLeave={() => setControlsVisible(false)}
        >
          <video
              ref={videoRef}
              style={{ width: '100%', height: '100%', objectFit: 'contain' }}
              onClick={togglePlayPause}
              onPlay={() => setIsPlaying(true)}
              onPause={() => setIsPlaying(false)}
              onTimeUpdate={(e) => setCurrentTime(e.currentTarget.currentTime)}
              onLoadedMetadata={(e) => setDuration(e.currentTarget.duration)}
          />
          <ControlBar sx={{ opacity: controlsVisible ? 1 : 0 }}>
            <IconButton onClick={togglePlayPause} sx={{ color: 'white' }}>
              {isPlaying ? <PauseIcon /> : <PlayArrowIcon />}
            </IconButton>
            <TimeText>{formatTime(currentTime)}</TimeText>
            <Slider
                value={currentTime}
                max={duration || 0}
                onChange={handleSeek}
                sx={{ color: 'white', mx: 2 }}
            />
            <TimeText>{formatTime(duration)}</TimeText>
            <IconButton onClick={toggleMute} sx={{ color: 'white' }}>
              {isMuted || volume === 0 ? <VolumeOffIcon /> : <VolumeUpIcon />}
            </IconButton>
            <Slider
                value={volume}
                max={1}
                step={0.1}
                onChange={handleVolumeChange}
                sx={{ width: 100, color: 'white', mx: 1 }}
            />
            {/* 画質選択（「自動」以外の画質がある場合のみ）。メニューはフルスクリーン中も見えるようコンテナ内に描画する */}
            {availableQualities.length > 1 && (
                <FormControl variant="standard" size="small" sx={{ minWidth: 120, ml: 1 }}>
                  <Select
                      value={currentQuality}
                      onChange={handleQualityChange}
                      sx={{ color: 'white', '& .MuiSvgIcon-root': { color: 'white' }, '&:before': { borderColor: 'white' }, '&:hover:not(.Mui-disabled):before': { borderColor: 'white' } }}
                      renderValue={(value) => (
                          <Typography sx={{ color: 'white' }}>
                            {value === 'auto' ? `自動 ${activeQualityLabel}` : availableQualities.find(q => q.level === value)?.label || ''}
                          </Typography>
                      )}
                      MenuProps={{
                        container: videoContainerRef.current,
                      }}
                  >
                    {availableQualities.map((q) => (
                        <MenuItem key={q.level} value={q.level === -1 ? 'auto' : q.level}>
                          {q.level === -1 ? `自動 ${activeQualityLabel}` : q.label}
                        </MenuItem>
                    ))}
                  </Select>
                </FormControl>
            )}
            <IconButton onClick={toggleFullscreen} sx={{ color: 'white' }}>
              {isFullscreen ? <FullscreenExitIcon /> : <FullscreenIcon />}
            </IconButton>
          </ControlBar>
        </Box>

        {/* 投稿者・投稿日と、編集・削除ボタン */}
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mt: 2 }}>
          <Typography variant="subtitle1" color="text.secondary">
            投稿者: {uploader?.username || 'Unknown User'} | 投稿日: {new Date(video.created_at).toLocaleDateString()}
          </Typography>
          {canEdit && (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                <Tooltip title="動画詳細を編集">
                  <IconButton component={RouterLink} to={`/edit-video/${video.id}`}><EditIcon /></IconButton>
                </Tooltip>
                <Button variant="contained" color="error" onClick={handleDelete}>削除</Button>
              </Box>
          )}
        </Box>
        <Typography variant="body1" sx={{ mt: 2, whiteSpace: 'pre-wrap' }}>
          {video.description}
        </Typography>
      </Container>
  );
};

export default VideoDetailPage;