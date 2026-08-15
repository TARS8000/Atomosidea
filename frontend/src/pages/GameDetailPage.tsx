import { useState, useEffect, useRef } from 'react';
import { useParams, useNavigate, Link as RouterLink, useLocation } from 'react-router-dom';
import axios from 'axios';
import { Container, Typography, Box, CircularProgress, Alert, Paper, Button, IconButton, Tooltip } from '@mui/material';
import FullscreenIcon from '@mui/icons-material/Fullscreen';
import FullscreenExitIcon from '@mui/icons-material/FullscreenExit';
import EditIcon from '@mui/icons-material/Edit';
import SettingsIcon from '@mui/icons-material/Settings';
import { useAuth } from '../context/AuthContext';

interface Game {
  id: string;
  title: string;
  description: string;
  game_url: string;
  status: string;
  processing_details: string | null;
  uploader_id: string; // ★ number から string (UUID) に修正
  created_at: string;
  scale: number;
  offset_x: number;
  offset_y: number;
  native_width: number;
  native_height: number;
}

interface Uploader {
  username: string;
}

const GameDetailPage = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const initialStatus = (location.state as { initialStatus?: string })?.initialStatus || 'unknown';

  const [game, setGame] = useState<Game | null>(null);
  const [uploader, setUploader] = useState<Uploader | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [currentStatus, setCurrentStatus] = useState<string>(initialStatus);
  const { user, token } = useAuth();
  const navigate = useNavigate();
  const gameContainerRef = useRef<HTMLDivElement>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [windowSize, setWindowSize] = useState({ width: window.innerWidth, height: window.innerHeight });

  useEffect(() => {
    let pollingInterval: number;

    const fetchGameStatus = async () => {
      if (!id) return;
      try {
        const gameRes = await axios.get(`/api/games/${id}`);
        const fetchedGame: Game = gameRes.data;
        setGame(fetchedGame);
        setCurrentStatus(fetchedGame.status);

        if (fetchedGame.status === 'public') {
          const uploaderId = fetchedGame.uploader_id;
          if (uploaderId != null) {
            const uploaderRes = await axios.get(`/api/profile/${uploaderId}`);
            setUploader(uploaderRes.data);
          } else {
            console.error("uploaderId is null or undefined in the game response data.");
          }
          clearInterval(pollingInterval);
        } else if (fetchedGame.status === 'rejected' || fetchedGame.status === 'error' || fetchedGame.status === 'invalid') {
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

    if (currentStatus !== 'public' && currentStatus !== 'rejected' && currentStatus !== 'error' && currentStatus !== 'invalid') {
      pollingInterval = setInterval(fetchGameStatus, 5000);
    }

    return () => clearInterval(pollingInterval);
  }, [id, currentStatus]);

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

  const handleToggleFullscreen = () => {
    if (!gameContainerRef.current) return;
    if (!document.fullscreenElement) {
      gameContainerRef.current.requestFullscreen();
    } else {
      document.exitFullscreen();
    }
  };

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
  const canEdit = user && (String(user.userID) === String(game.uploader_id) || user.isAdmin);

  const nativeWidth = game.native_width || 960;
  const nativeHeight = game.native_height || 600;

  let transformStyle: string;

  if (isFullscreen) {
    const scale = Math.min(windowSize.width / nativeWidth, windowSize.height / nativeHeight);
    transformStyle = `scale(${scale})`;
  } else {
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