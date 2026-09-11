import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

const EditVideoPage = () => {
  const { id } = useParams();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [existingThumbnailUrl, setExistingThumbnailUrl] = useState('');
  const [loading, setLoading] = useState(true);
  const [updating, setUpdating] = useState(false);
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
      } catch (err) {
        setError('ビデオ情報の取得に失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchVideo();
  }, [id, token]);

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      setThumbnail(e.target.files[0]);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setUpdating(true);
    setError('');
    setSuccess('');

    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    try {
      // Note: axios will set the correct Content-Type for FormData
      await axios.put(`/api/videos/${id}`, formData, {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      });
      setSuccess('ビデオ情報が更新されました。');
      setTimeout(() => navigate(`/videos/${id}`), 2000);
    } catch (err) {
      setError('更新に失敗しました。');
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
        {existingThumbnailUrl && (
          <Box mb={2}>
            <Typography variant="subtitle1">現在のサムネイル</Typography>
            <img src={existingThumbnailUrl} alt="Thumbnail" style={{ maxWidth: '100%', height: 'auto' }} />
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
