import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

const MAX_POLL_MS = 120000;

const EditVideoPage = () => {
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
    const fetchVideo = async () => {
      try {
        const response = await axios.get(`/api/videos/${id}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        setTitle(response.data.title);
        setDescription(response.data.description);
        setExistingThumbnailUrl(response.data.thumbnail_path);
        setThumbnailUrl(response.data.thumbnail_path);
      } catch (err) {
        setError('ビデオ情報の取得に失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchVideo();
  }, [id, token]);

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      setThumbnail(file);
      setThumbnailUrl(URL.createObjectURL(file));
    }
  };

  const pollVideo = async (signal: AbortSignal): Promise<{ thumbnailUrl?: string }> => {
    return new Promise((resolve, reject) => {
      const startTime = Date.now();
      const pollInterval = setInterval(async () => {
        try {
          const videoRes = await axios.get(`/api/videos/${id}`, { signal });
          const video = videoRes.data;
          if (!video.thumbnail_sfsp_job_id) {
            clearInterval(pollInterval);
            resolve({ thumbnailUrl: video.thumbnail_path || existingThumbnailUrl });
          }
        } catch (err) {
          if (!axios.isCancel(err)) {
            console.error('Polling video failed:', err);
          }
        }
        if (Date.now() - startTime > MAX_POLL_MS) {
          clearInterval(pollInterval);
          reject(new Error('サムネイルアップロードの待機がタイムアウトしました。'));
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
        const res = await axios.put(`/api/videos/${id}`, formData, {
          headers: {
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          const result = await pollVideo(controller.signal);
          if (result.thumbnailUrl) {
            newThumbnailUrl = result.thumbnailUrl;
          }
        } else if (res.data.thumbnail_path) {
          newThumbnailUrl = res.data.thumbnail_path;
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
        setSuccess('ビデオ情報が更新されました。');
        setTimeout(() => navigate(`/videos/${id}`), 2000);
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
      await axios.delete(`/api/videos/${id}/thumbnail`, {
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
        ビデオ情報を編集
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
          <Button variant="contained" component="label">
            新しいサムネイルを選択
            <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
          </Button>
          {thumbnail && <Typography sx={{ ml: 2, display: 'inline' }}>{thumbnail.name}</Typography>}
        </Box>
        {thumbnailUrl && (
          <Box mb={2}>
            <Typography variant="subtitle1">現在のサムネイル</Typography>
            <Box sx={{ position: 'relative', display: 'inline-block', maxWidth: '100%' }}>
              <img
                src={thumbnailUrl}
                alt="Thumbnail"
                style={{ maxWidth: '100%', height: 'auto', display: 'block' }}
              />
              {isScanningThumbnail && (
                <Box
                  sx={{
                    position: 'absolute',
                    inset: 0,
                    bgcolor: 'rgba(0,0,0,0.6)',
                    display: 'flex',
                    justifyContent: 'center',
                    alignItems: 'center',
                  }}
                >
                  <CircularProgress size={20} sx={{ color: 'white', mr: 1 }} />
                  <Typography variant="body2" sx={{ color: 'white' }}>セキュリティスキャン中...</Typography>
                </Box>
              )}
            </Box>
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

export default EditVideoPage;
