import { useState, useEffect, useRef } from 'react';
import { useParams, useNavigate, Link as RouterLink, useLocation } from 'react-router-dom';
import axios from 'axios';
import { Container, Typography, Box, CircularProgress, Alert, Paper, Button, IconButton, Tooltip } from '@mui/material';
import FullscreenIcon from '@mui/icons-material/Fullscreen';
import FullscreenExitIcon from '@mui/icons-material/FullscreenExit';
import EditIcon from '@mui/icons-material/Edit';
import SettingsIcon from '@mui/icons-material/Settings';
import { useAuth } from '../context/AuthContext';

/** GET /api/games/:id が返すゲーム情報。 */
interface Game {
  id: string;
  title: string;
  description: string;
  game_url: string;
  status: string;
  /** 処理中の進捗メッセージ（投稿者以外には null で返る）。 */
  processing_details: string | null;
  uploader_id: string; // ★ number から string (UUID) に修正
  created_at: string;
  /** 投稿者が調整画面で保存した拡大率・オフセット（通常表示時のみ使う）。 */
  scale: number;
  offset_x: number;
  offset_y: number;
  /** ゲーム本来の幅・高さ（px）。未設定なら偽値になる。 */
  native_width: number;
  native_height: number;
}

/** GET /api/profile/:id から投稿者名だけを使う。 */
interface Uploader {
  username: string;
}

/**
 * ゲームの詳細ページ。iframe でゲームを表示し、処理完了（public）までは状態をポーリングする。
 * 投稿者または管理者には編集・表示調整・削除の操作を表示する。
 */
const GameDetailPage = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  // アップロード直後の遷移では location.state で初期 status が渡される（無ければ 'unknown'）
  const initialStatus = (location.state as { initialStatus?: string })?.initialStatus || 'unknown';

  const [game, setGame] = useState<Game | null>(null);
  const [uploader, setUploader] = useState<Uploader | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [currentStatus, setCurrentStatus] = useState<string>(initialStatus);
  const { user, token } = useAuth();
  const navigate = useNavigate();
  // フルスクリーン表示の対象となるゲーム枠
  const gameContainerRef = useRef<HTMLDivElement>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  // フルスクリーン時の倍率計算に使うウィンドウサイズ
  const [windowSize, setWindowSize] = useState({ width: window.innerWidth, height: window.innerHeight });

  // ゲーム情報を取得し、終端状態になるまで 5 秒ごとにポーリングする。
  // currentStatus が変わるたびに effect が再実行され、取得とポーリング設定をやり直す。
  useEffect(() => {
    let pollingInterval: number;

    // ゲーム情報を 1 回取得し、status に応じて投稿者情報の取得やポーリング停止を行う。
    const fetchGameStatus = async () => {
      if (!id) return;
      try {
        const gameRes = await axios.get(`/api/games/${id}`);
        const fetchedGame: Game = gameRes.data;
        setGame(fetchedGame);
        setCurrentStatus(fetchedGame.status);

        if (fetchedGame.status === 'public') {
          // 公開済み: 投稿者のプロフィールを取得してポーリングを止める
          const uploaderId = fetchedGame.uploader_id;
          if (uploaderId != null) {
            const uploaderRes = await axios.get(`/api/profile/${uploaderId}`);
            setUploader(uploaderRes.data);
          } else {
            console.error("uploaderId is null or undefined in the game response data.");
          }
          clearInterval(pollingInterval);
        } else if (['rejected', 'error', 'invalid', 'failed', 'quarantined'].includes(fetchedGame.status)) {
          // 失敗系の終端状態: エラーを表示してポーリングを止める
          setError(`ゲームの処理中にエラーが発生しました: ${fetchedGame.status}`);
          clearInterval(pollingInterval);
        }
      } catch (err) {
        setError('ゲーム詳細の読み込みに失敗しました。');
        console.error('Failed to fetch game details:', err);
        clearInterval(pollingInterval);
      } finally {
        setLoading(false);
      }
    };

    fetchGameStatus();

    // 終端状態でなければポーリングを開始する
    if (!['public', 'rejected', 'error', 'invalid', 'failed', 'quarantined'].includes(currentStatus)) {
      pollingInterval = setInterval(fetchGameStatus, 5000);
    }

    return () => clearInterval(pollingInterval);
  }, [id, currentStatus]);

  // フルスクリーンの切り替えとウィンドウのリサイズを監視し、表示状態とサイズを更新する。
  useEffect(() => {
    const handleStateChange = () => {
      setIsFullscreen(!!document.fullscreenElement);
      setWindowSize({ width: window.innerWidth, height: window.innerHeight });
    };
    document.addEventListener('fullscreenchange', handleStateChange);
    window.addEventListener('resize', handleStateChange);
    return () => {
      document.removeEventListener('fullscreenchange', handleStateChange);
      window.removeEventListener('resize', handleStateChange);
    };
  }, []);

  /** ゲーム枠のフルスクリーン表示を切り替える。 */
  const handleToggleFullscreen = () => {
    if (!gameContainerRef.current) return;
    if (!document.fullscreenElement) {
      gameContainerRef.current.requestFullscreen();
    } else {
      document.exitFullscreen();
    }
  };

  /** 確認ダイアログの後、DELETE /api/games/:id でゲームを削除してトップページへ遷移する。 */
  const handleDelete = async () => {
    if (!window.confirm('本当にこのゲームを削除しますか？')) return;
    try {
      await axios.delete(`/api/games/${id}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      alert('ゲームを削除しました。');
      navigate('/');
    } catch (err) {
      setError('ゲームの削除に失敗しました。');
    }
  };

  // 読み込み中・スキャン中・処理中は status に応じた進捗メッセージを表示する
  if (loading || currentStatus === 'unknown' || currentStatus === 'scanning' || currentStatus === 'processing') {
    let message = 'ゲームを読み込み中...';
    if (currentStatus === 'scanning') {
      message = 'セキュリティスキャンを実行中...';
    } else if (currentStatus === 'processing') {
      message = game?.processing_details || 'ゲームファイルを準備中...';
    }
    return (
        <Container sx={{ textAlign: 'center', mt: 5 }}>
          <CircularProgress size={60} />
          <Typography variant="h6" sx={{ mt: 2 }}>{message}</Typography>
          <Typography variant="body2" color="text.secondary">しばらくお待ちください。</Typography>
        </Container>
    );
  }

  if (error) return <Alert severity="error">{error}</Alert>;
  if (!game || game.status !== 'public') {
    return (
        <Container>
          <Alert severity="error">ゲームの読み込みに失敗しました。ステータス: {game?.status || '不明'}</Alert>
        </Container>
    );
  }

  // ★ 比較箇所を String() に変換して安全に比較するように修正
  // 投稿者本人または管理者のみ編集・削除できる（UI 上の表示制御。実際の権限はバックエンドで検証される）
  const canEdit = user && (String(user.userID) === String(game.uploader_id) || user.isAdmin);

  // ゲーム本来のサイズ（未設定時は 960x600 とみなす）
  const nativeWidth = game.native_width || 960;
  const nativeHeight = game.native_height || 600;

  let transformStyle: string;

  if (isFullscreen) {
    // フルスクリーン時: 画面に収まる最大倍率で中央表示する（保存済みの調整値は使わない）
    const scale = Math.min(windowSize.width / nativeWidth, windowSize.height / nativeHeight);
    transformStyle = `scale(${scale})`;
  } else {
    // 通常時: 800x450 の枠に収まる基準倍率に、保存済みの拡大率とオフセットを適用する
    const targetDisplayWidth = 800;
    const targetDisplayHeight = 450;
    const baseScale = Math.min(targetDisplayWidth / nativeWidth, targetDisplayHeight / nativeHeight);
    const finalScale = baseScale * (game.scale || 1);
    transformStyle = `translate(${game.offset_x || 0}px, ${game.offset_y || 0}px) scale(${finalScale})`;
  }

  return (
      <Container maxWidth="xl">
        <Typography variant="h4" component="h1" gutterBottom>
          {game.title}
        </Typography>

        {/* ゲーム枠: 通常は 800x450、フルスクリーン時は画面全体に広げる */}
        <Paper
            ref={gameContainerRef}
            elevation={3}
            sx={{
              width: '800px',
              height: '450px',
              maxWidth: '100%',
              mx: 'auto',
              mb: 3,
              overflow: 'hidden',
              position: 'relative',
              backgroundColor: '#000',
              display: 'flex',
              justifyContent: 'center',
              alignItems: 'center',
              '&:fullscreen': {
                width: '100%',
                height: '100%',
                maxWidth: '100%',
              },
            }}
        >
          {/* ゲーム本来のサイズで描画し、CSS transform で移動・拡縮する */}
          <Box
              sx={{
                width: `${nativeWidth}px`,
                height: `${nativeHeight}px`,
                transform: transformStyle,
                transformOrigin: 'center center',
                flexShrink: 0,
                transition: 'transform 0.3s ease-out',
              }}
          >
            <iframe
                key={game.id}
                src={game.game_url}
                title={game.title}
                style={{
                  width: '100%',
                  height: '100%',
                  border: 0,
                }}
                allow="fullscreen"
                allowFullScreen
            />
          </Box>
        </Paper>

        {/* 投稿者・投稿日と操作ボタン（編集・表示調整・フルスクリーン・削除） */}
        <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', my: 2 }}>
          <Typography variant="subtitle1" color="text.secondary">
            投稿者: {uploader?.username || 'Unknown User'} | 投稿日: {new Date(game.created_at).toLocaleDateString()}
          </Typography>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            {canEdit && (
                <>
                  <Tooltip title="タイトル/説明/サムネイルを編集">
                    <IconButton component={RouterLink} to={`/edit-game/${game.id}`}>
                      <EditIcon />
                    </IconButton>
                  </Tooltip>
                  <Tooltip title="表示を調整 (ズーム/位置)">
                    <IconButton component={RouterLink} to={`/adjust-game/${game.id}`}>
                      <SettingsIcon />
                    </IconButton>
                  </Tooltip>
                </>
            )}
            <IconButton onClick={handleToggleFullscreen} title={isFullscreen ? 'フルスクリーンを終了' : 'フルスクリーン'}>
              {isFullscreen ? <FullscreenExitIcon /> : <FullscreenIcon />}
            </IconButton>
            {canEdit && (
                <Button variant="contained" color="error" onClick={handleDelete}>
                  削除
                </Button>
            )}
          </Box>
        </Box>
        <Typography variant="body1" sx={{ mt: 2, whiteSpace: 'pre-wrap' }}>
          {game.description}
        </Typography>
      </Container>
  );
};

export default GameDetailPage;