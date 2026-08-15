import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert, LinearProgress } from '@mui/material';
import { useAuth } from '../context/AuthContext';

const UploadGamePage = () => {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState<number>(0);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const navigate = useNavigate();
  const { token } = useAuth();

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      setFile(e.target.files[0]);
    }
  };

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      const imageFile = e.target.files[0];
      if (imageFile.size > 100 * 1024 * 1024) { // 100MB limit
        setError('サムネイル画像のサイズは100MB未満である必要があります。');
        return;
      }
      setThumbnail(imageFile);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file) {
      setError('アップロードするゲームファイルを選択してください。');
      return;
    }

    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    formData.append('game', file);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    setUploading(true);
    setError('');
    setSuccess('');
    setUploadProgress(0);

    try {
      const response = await axios.post('/api/games/upload', formData, {
        headers: {
          'Content-Type': 'multipart/form-data',
          Authorization: `Bearer ${token}`,
        },
        onUploadProgress: (progressEvent) => {
          const percentCompleted = Math.round((progressEvent.loaded * 100) / (progressEvent.total ?? 1));
          setUploadProgress(percentCompleted);
        },
      });
      setSuccess(response.data.message);
      const { gameId } = response.data;
      if (gameId) {
        navigate(`/adjust-game/${gameId}`);
      } else {
        setError('アップロード後にゲームIDを取得できませんでした。調整ページにリダイレクトできません。');
      }
    } catch (err) {
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || '不明なエラーが発生しました。');
      } else {
        setError('不明なエラーが発生しました。');
      }
    } finally {
      setUploading(false);
    }
  };

  return (
    <Container maxWidth="sm">
      <Typography variant="h4" component="h1" gutterBottom>
        ゲームをアップロード
        <br />
        (Unity WebGL版のみ)
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
            ゲームファイルを選択 (.zip)
            <input type="file" hidden accept=".zip" onChange={handleFileChange} />
          </Button>
          {file && <Typography sx={{ ml: 2, display: 'inline' }}>{file.name}</Typography>}
        </Box>
        <Box mb={2}>
          <Button variant="contained" component="label">
            サムネイル画像を選択
            <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
          </Button>
          {thumbnail && <Typography sx={{ ml: 2, display: 'inline' }}>{thumbnail.name}</Typography>}
        </Box>
        {uploading && (
          <Box sx={{ width: '100%', mt: 2 }}>
            <LinearProgress variant="determinate" value={uploadProgress} />
            <Typography variant="body2" color="text.secondary" align="center">{`${uploadProgress}%`}</Typography>
          </Box>
        )}
        <Button type="submit" variant="contained" color="primary" disabled={uploading}>
          {uploading ? <CircularProgress size={24} /> : 'アップロード'}
        </Button>
        {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
        {success && <Alert severity="success" sx={{ mt: 2 }}>{success}</Alert>}
      </form>
    </Container>
  );
};

export default UploadGamePage;