import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert, Paper, Grid } from '@mui/material';
import { useAuth } from '../context/AuthContext';

interface Game {
  title: string;
  description: string;
  thumbnail_url: string;
}

const EditGamePage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { token } = useAuth();

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [thumbnail, setThumbnail] = useState<File | null>(null);
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
    const fetchGame = async () => {
      try {
        setLoading(true);
        const response = await axios.get(`/api/games/${id}`);
        const game: Game = response.data;
        setTitle(game.title);
        setDescription(game.description);
        setCurrentThumbnail(game.thumbnail_url);
      } catch (err) {
        setError('ゲーム詳細の読み込みに失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchGame();
  }, [id]);

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      const imageFile = e.target.files[0];
      if (imageFile.size > 100 * 1024 * 1024) { // 100MB limit
        setError('サムネイル画像のサイズは100MB未満である必要があります。');
        return;
      }
      setThumbnail(imageFile);
      setCurrentThumbnail(URL.createObjectURL(imageFile)); // Show preview of new thumbnail
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    setSaving(true);
    setError('');
    setSuccess('');

    try {
      await axios.put(`/api/games/${id}`, formData, {
        headers: {
          'Content-Type': 'multipart/form-data',
          Authorization: `Bearer ${token}`,
        },
      });
      setSuccess('ゲーム詳細を更新しました！');
      setTimeout(() => navigate(`/games/${id}`), 1500);
    } catch (err) {
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || '不明なエラーが発生しました。');
      } else {
        setError('不明なエラーが発生しました。');
      }
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <CircularProgress />;
  if (error && !title) return <Alert severity="error">{error}</Alert>;

  return (
    <Container maxWidth="md">
      <Typography variant="h4" component="h1" gutterBottom>
        ゲーム詳細を編集
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
            <Paper variant="outlined" sx={{ p: 1, mb: 2, height: 194, display: 'flex', justifyContent: 'center', alignItems: 'center' }}>
              {currentThumbnail ? (
                <img src={currentThumbnail} alt="Thumbnail preview" style={{ maxHeight: '100%', maxWidth: '100%' }} />
              ) : (
                <Typography color="text.secondary">サムネイルなし</Typography>
              )}
            </Paper>
            <Button variant="contained" component="label" fullWidth>
              サムネイルを変更
              <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
            </Button>
          </Grid>
        </Grid>
        <Box mt={4} display="flex" justifyContent="center">
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

export default EditGamePage;
