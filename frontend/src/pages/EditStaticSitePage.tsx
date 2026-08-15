import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert, Paper, Grid } from '@mui/material';
import { useAuth } from '../context/AuthContext';

interface StaticSite {
  id: string;
  user_id: string;
  title: string;
  description: string;
  minio_path: string;
  entry_point_path: string;
  thumbnail_url: string;
  status: string;
  created_at: string;
  updated_at: string;
}

const EditStaticSitePage = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { token } = useAuth();

  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [currentThumbnail, setCurrentThumbnail] = useState<string>(''); // Static sites might have thumbnails too
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  useEffect(() => {
    if (!id) {
      setLoading(true);
      return;
    }
    const fetchStaticSite = async () => {
      try {
        setLoading(true);
        const response = await axios.get(`/api/static-sites/${id}`);
        const staticSite: StaticSite = response.data;
        setTitle(staticSite.title);
        setDescription(staticSite.description);
        setCurrentThumbnail(staticSite.thumbnail_url); // Assuming static sites can have thumbnails
      } catch (err) {
        setError('静的サイト詳細の読み込みに失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchStaticSite();
  }, [id]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    setSaving(true);
    setError('');
    setSuccess('');

    try {
      await axios.put(`/api/static-sites/${id}`, { title, description }, {
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'application/json', // Send as JSON
        },
      });
      setSuccess('静的サイト詳細を更新しました！');
      setTimeout(() => navigate(`/static-sites/${id}`), 1500); // Adjust redirect path if needed
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
  if (error && !title) return <Alert severity="error">{error}</Alert>; // Only show error if no title (initial load error)

  return (
    <Container maxWidth="md">
      <Typography variant="h4" component="h1" gutterBottom>
        静的サイト詳細を編集
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
            {/* Static sites might not have editable thumbnails via this page, so keeping it simple for now */}
            {/* <Button variant="contained" component="label" fullWidth>
              サムネイルを変更
              <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
            </Button> */}
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

export default EditStaticSitePage;