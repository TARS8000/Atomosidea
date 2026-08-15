import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert, Paper, Grid } from '@mui/material';
import { useAuth } from '../context/AuthContext';

interface Video {
  title: string;
  description: string;
  thumbnail_path?: string;
}

const EditVideoPage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { token } = useAuth();

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [currentThumbnail, setCurrentThumbnail] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  useEffect(() => {
    if (!id) {
      setLoading(true);
      return;
    }
    const fetchVideo = async () => {
      try {
        setLoading(true);
        const response = await axios.get(`/api/videos/${id}`);
        const video: Video = response.data;
        setTitle(video.title);
        setDescription(video.description || '');
        if (video.thumbnail_path) {
          setCurrentThumbnail(video.thumbnail_path);
        }
      } catch (err) {
        setError('動画詳細の読み込みに失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchVideo();
  }, [id]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!title.trim()) {
      setError('タイトルを入力してください。');
      return;
    }

    setSaving(true);
    setError('');
    setSuccess('');

    try {
      // バックエンドの JSON バインディング形式に合わせて送信
      await axios.put(
          `/api/videos/${id}`,
          {
            title: title,
            description: description,
          },
          {
            headers: {
              'Content-Type': 'application/json',
              Authorization: `Bearer ${token}`,
            },
          }
      );

      setSuccess('動画詳細を更新しました！');
      setTimeout(() => navigate(`/videos/${id}`), 1500);
    } catch (err) {
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || '動画の更新に失敗しました。');
      } else {
        setError('不明なエラーが発生しました。');
      }
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
        <Box display="flex" justifyContent="center" my={4}>
          <CircularProgress />
        </Box>
    );
  }

  if (error && !title) {
    return (
        <Container maxWidth="md">
          <Alert severity="error">{error}</Alert>
        </Container>
    );
  }

  return (
      <Container maxWidth="md">
        <Typography variant="h4" component="h1" gutterBottom sx={{ mt: 2 }}>
          動画詳細を編集
        </Typography>
        <form onSubmit={handleSave}>
          <Grid container spacing={3}>
            <Grid item xs={12} md={8}>
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
                    rows={10}
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                />
              </Box>
            </Grid>
            <Grid item xs={12} md={4}>
              <Typography variant="subtitle1" gutterBottom>サムネイル</Typography>
              <Paper
                  variant="outlined"
                  sx={{
                    p: 1,
                    mb: 2,
                    height: 194,
                    display: 'flex',
                    justifyContent: 'center',
                    alignItems: 'center',
                    backgroundColor: '#f5f5f5',
                  }}
              >
                {currentThumbnail ? (
                    <img src={currentThumbnail} alt="Thumbnail preview" style={{ maxHeight: '100%', maxWidth: '100%' }} />
                ) : (
                    <Typography color="text.secondary">サムネイルなし</Typography>
                )}
              </Paper>
            </Grid>
          </Grid>
          <Box mt={4} display="flex" justifyContent="center" gap={2}>
            <Button
                variant="outlined"
                size="large"
                onClick={() => navigate(`/videos/${id}`)}
                disabled={saving}
            >
              キャンセル
            </Button>
            <Button type="submit" variant="contained" color="primary" size="large" disabled={saving}>
              {saving ? <CircularProgress size={24} /> : '変更を保存'}
            </Button>
          </Box>
          {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
          {success && <Alert severity="success" sx={{ mt: 2 }}>{success}</Alert>}
        </form>
      </Container>
  );
};

export default EditVideoPage;