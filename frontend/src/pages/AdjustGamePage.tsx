import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, Typography, Box, CircularProgress, Alert, Paper, Button, Slider, Grid } from '@mui/material';
import { useAuth } from '../context/AuthContext';

interface Game {
  id: string; // Changed from number to string
  title: string;
  description: string;
  game_url: string;
  status: string;
  processing_details: string; // Added
  scale: number;
  offset_x: number;
  offset_y: number;
  native_width: number;
  native_height: number;
}

const AdjustGamePage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { token } = useAuth();

  const [game, setGame] = useState<Game | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [scale, setScale] = useState(1.0);
  const [offsetX, setOffsetX] = useState(0);
  const [offsetY, setOffsetY] = useState(0);

  useEffect(() => {
    if (!id) {
      setLoading(true);
      return;
    }

    let pollingInterval: number | null = null;

    const stopPolling = () => {
      if (pollingInterval) {
        clearInterval(pollingInterval);
        pollingInterval = null;
      }
    };

    const fetchGame = async () => {
      try {
        const response = await axios.get(`/api/games/${id}`);
        const fetchedGame: Game = response.data;
        setGame(fetchedGame);

        if (fetchedGame.status === 'public') {
          setScale(fetchedGame.scale);
          setOffsetX(fetchedGame.offset_x);
          setOffsetY(fetchedGame.offset_y);
          setLoading(false);
          stopPolling();
        } else if (fetchedGame.status === 'error') {
          setError(`ゲーム処理に失敗しました: ${fetchedGame.description}`);
          setLoading(false);
          stopPolling();
        } else {
          setLoading(true);
        }
      } catch (err) {
        setError('ゲーム詳細の読み込みに失敗しました。');
        setLoading(false);
        stopPolling();
      }
    };

    fetchGame();
    pollingInterval = window.setInterval(fetchGame, 2000); // Poll every 2 seconds

    return () => {
      stopPolling();
    };
  }, [id]);

  const handleSave = async () => {
    try {
      await axios.put(
        `/api/games/adjust/${id}`,
        { scale, offset_x: offsetX, offset_y: offsetY },
        { headers: { Authorization: `Bearer ${token}` } }
      );
      alert('調整を保存しました！');
      navigate(`/games/${id}`);
    } catch (err) {
      setError('調整の保存に失敗しました。');
    }
  };

  if (loading || (game && game.status === 'processing')) {
    return (
      <Container>
        <Typography variant="h5" align="center">ゲームを準備中です...</Typography>
        <Box sx={{ display: 'flex', justifyContent: 'center', my: 4 }}>
          <CircularProgress />
        </Box>
        <Typography align="center" color="text.secondary">
          {game?.processing_details || 'この処理には数分かかることがあります。ページは自動的に更新されます。'}
        </Typography>
      </Container>
    );
  }

  if (error) return <Alert severity="error">{error}</Alert>;
  if (!game) return <Alert severity="warning">ゲームが見つかりません。</Alert>;

  const targetDisplayWidth = 800;
  const targetDisplayHeight = 450;
  const nativeWidth = game.native_width || 960;
  const nativeHeight = game.native_height || 720;

  const baseScale = Math.min(
    targetDisplayWidth / nativeWidth,
    targetDisplayHeight / nativeHeight
  );
  const finalIframeScale = baseScale * scale;

  return (
    <Container maxWidth="lg">
      <Typography variant="h4" gutterBottom>
        ゲーム表示の調整: {game.title}
      </Typography>
      <Typography paragraph>
        スライダーを使って、ゲームの表示倍率と位置を調整してください。800x450のフレーム内にゲームが完璧に収まるようにします。
      </Typography>

      <Paper
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
        }}
      >
        <Box
          sx={{
            width: `${nativeWidth}px`,
            height: `${nativeHeight}px`,
            transform: `translate(${offsetX}px, ${offsetY}px) scale(${finalIframeScale})`,
            transformOrigin: 'center center',
            flexShrink: 0,
          }}
        >
          <iframe
            src={game.game_url}
            title="Game Preview"
            style={{
              width: '100%',
              height: '100%',
              border: 0,
            }}
          />
        </Box>
      </Paper>

      <Grid container spacing={4} alignItems="center">
        <Grid item xs={12} md={4}>
          <Typography gutterBottom>拡大率 (Scale)</Typography>
          <Slider
            value={scale}
            onChange={(_, newValue) => setScale(newValue as number)}
            min={0.5}
            max={2.0}
            step={0.01}
            valueLabelDisplay="auto"
          />
        </Grid>
        <Grid item xs={12} md={4}>
          <Typography gutterBottom>横位置 (Offset X)</Typography>
          <Slider
            value={offsetX}
            onChange={(_, newValue) => setOffsetX(newValue as number)}
            min={-200}
            max={200}
            step={1}
            valueLabelDisplay="auto"
          />
        </Grid>
        <Grid item xs={12} md={4}>
          <Typography gutterBottom>縦位置 (Offset Y)</Typography>
          <Slider
            value={offsetY}
            onChange={(_, newValue) => setOffsetY(newValue as number)}
            min={-200}
            max={200}
            step={1}
            valueLabelDisplay="auto"
          />
        </Grid>
      </Grid>

      <Box mt={4} display="flex" justifyContent="center">
        <Button variant="contained" color="primary" size="large" onClick={handleSave}>
          調整を保存
        </Button>
      </Box>
    </Container>
  );
};

export default AdjustGamePage;