import { useState } from 'react';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, LinearProgress, Alert, CircularProgress } from '@mui/material';
import { useAuth } from '../context/AuthContext';
import { useNavigate } from 'react-router-dom';

const UploadPage = () => {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [uploadProgress, setUploadProgress] = useState(0);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const { token } = useAuth();
  const navigate = useNavigate();

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
      setError('アップロードする動画ファイルを選択してください。');
      return;
    }

    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    formData.append('video', file);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    setUploading(true);
    setError('');
    setSuccess('');
    setUploadProgress(0);

    try {
      const response = await axios.post('/api/videos/upload', formData, {
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
      navigate(`/videos/${response.data.videoID}`);
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
        動画をアップロード
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
            動画ファイルを選択
            <input type="file" hidden accept="video/mp4,video/webm" onChange={handleFileChange} />
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
          <Box sx={{ width: '100%', my: 2 }}>
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

export default UploadPage;