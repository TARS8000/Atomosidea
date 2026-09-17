import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

const EditGamePage = () => {
  const { id } = useParams();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [existingThumbnailUrl, setExistingThumbnailUrl] = useState('');
  const [thumbnailUrl, setThumbnailUrl] = useState('');
  const [loading, setLoading] = useState(true);
  const [updating, setUpdating] = useState(false);
  const [isScanningThumbnail, setIsScanningThumbnail] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const navigate = useNavigate();
  const { token } = useAuth();

  useEffect(() => {
    const fetchGame = async () => {
      try {
        const response = await axios.get(`/api/games/${id}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        setTitle(response.data.title);
        setDescription(response.data.description);
        setExistingThumbnailUrl(response.data.thumbnail_url);
        setThumbnailUrl(response.data.thumbnail_url);
      } catch (err) {
        setError('ゲーム情報の取得に失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchGame();
  }, [id, token]);

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      setThumbnail(file);
      setThumbnailUrl(URL.createObjectURL(file));
    }
  };

  // thumbnail_sfsp_job_id が NULL になるまでポーリングする。
  // このフラグがクリアされるのはゲームワーカーがクリーンなサムネイルを
  // game-storage に書き込んだ後だけなので、これでストレ書き込み完了を
  // 保証でき、フライングによる旧画像の表示を防止できる。
  const MAX_POLL_MS = 120000;
  const pollGame = async (signal: AbortSignal): Promise<{ thumbnailUrl?: string }> => {
    return new Promise((resolve, reject) => {
      const startedAt = Date.now();
      const pollInterval = setInterval(async () => {
        try {
          const gameRes = await axios.get(`/api/games/${id}`, { signal });
          const game = gameRes.data;
          // ゲームワーカーがサムネイル処理を完了（job_id クリア）すれば解決。
          // この時点で thumbnail_url は新サムネイルに更新されている。
          if (!game.thumbnail_sfsp_job_id) {
            clearInterval(pollInterval);
            resolve({ thumbnailUrl: game.thumbnail_url || existingThumbnailUrl });
            return;
          }
          // 安全網: ゲームワーカーが応答しない無限スピンを防止する。
          if (Date.now() - startedAt > MAX_POLL_MS) {
            clearInterval(pollInterval);
            resolve({ thumbnailUrl: game.thumbnail_url || existingThumbnailUrl });
          }
        } catch (err) {
          if (!axios.isCancel(err)) {
            console.error('Polling game failed:', err);
          }
        }
      }, 2000);

      signal.addEventListener('abort', () => {
        clearInterval(pollInterval);
        reject(new Error('Polling aborted'));
      });
    });
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setUpdating(true);
    setError('');
    setSuccess('');

    const controller = new AbortController();

    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    try {
      let newThumbnailUrl = thumbnailUrl;

      if (thumbnail) {
        setIsScanningThumbnail(true);
        const res = await axios.put(`/api/games/${id}`, formData, {
          headers: {
            'Content-Type': 'multipart/form-data',
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          await pollGame(controller.signal);
        } else if (res.data.thumbnail_url) {
          newThumbnailUrl = res.data.thumbnail_url;
        }
      }

      // Preload the freshly-written thumbnail (cache-buster to bypass any stale
      // browser cache of the old image) and keep the overlay up until it has
      // loaded, then swap the displayed image only once it is fully ready, so
      // the old thumbnail never flashes during the transition.
      const bust = newThumbnailUrl.includes('?') ? '&' : '?';
      const freshUrl = newThumbnailUrl + bust + 't=' + Date.now();
      const preload = new Image();
      const reveal = () => {
        setIsScanningThumbnail(false);
        setThumbnailUrl(freshUrl);
        setSuccess('ゲーム情報が更新されました。');
        setTimeout(() => navigate(`/games/${id}`), 2000);
      };
      preload.onload = reveal;
      preload.onerror = reveal;
      preload.src = freshUrl;
    } catch (err) {
      if (!axios.isCancel(err)) {
        setError('更新に失敗しました。');
      }
      setIsScanningThumbnail(false);
    } finally {
      setUpdating(false);
    }
  };

  const handleDeleteThumbnail = async () => {
    if (!window.confirm('本当にサムネイルを削除しますか？')) {
      return;
    }
    setUpdating(true);
    try {
      await axios.delete(`/api/games/${id}/thumbnail`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      setExistingThumbnailUrl('');
      setThumbnailUrl('');
      setSuccess('サムネイルが削除されました。');
    } catch (err) {
      setError('サムネイルの削除に失敗しました。');
    } finally {
      setUpdating(false);
    }
  };

  if (loading) {
    return <CircularProgress />;
  }

  return (
    <Container maxWidth="sm">
      <Typography variant="h4" component="h1" gutterBottom>
        ゲーム情報を編集
      </Typography>
      <form onSubmit={handleSubmit}>
        <Box mb={2}>
          <TextField
            label="タイトル"
            variant="outlined"
            fullWidth
            required
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </Box>
        <Box mb={2}>
          <TextField
            label="説明"
            variant="outlined"
            fullWidth
            multiline
            rows={4}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Box>
        <Box mb={2}>
          <Box sx={{ position: 'relative', display: 'inline-block' }}>
            <Button variant="contained" component="label">
              新しいサムネイルを選択
              <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
            </Button>
            {isScanningThumbnail && (
              <Box
                sx={{
                  position: 'absolute',
                  inset: 0,
                  bgcolor: 'rgba(0,0,0,0.6)',
                  display: 'flex',
                  justifyContent: 'center',
                  alignItems: 'center',
                  borderRadius: 2,
                }}
              >
                <CircularProgress size={20} sx={{ color: 'white', mr: 1 }} />
                <Typography variant="body2" sx={{ color: 'white' }}>スキャン中...</Typography>
              </Box>
            )}
          </Box>
          {thumbnail && <Typography sx={{ ml: 2, display: 'inline' }}>{thumbnail.name}</Typography>}
        </Box>
        {thumbnailUrl && (
          <Box mb={2}>
            <Typography variant="subtitle1">現在のサムネイル</Typography>
            <img src={thumbnailUrl} alt="Thumbnail" style={{ maxWidth: '100%', height: 'auto' }} />
            <Button variant="outlined" color="secondary" onClick={handleDeleteThumbnail} sx={{ mt: 1 }}>
              サムネイルを削除
            </Button>
          </Box>
        )}
        <Button type="submit" variant="contained" color="primary" disabled={updating}>
          {updating ? <CircularProgress size={24} /> : '更新'}
        </Button>
        {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
        {success && <Alert severity="success" sx={{ mt: 2 }}>{success}</Alert>}
      </form>
    </Container>
  );
};

export default EditGamePage;
