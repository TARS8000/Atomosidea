import React, { useState } from 'react';
import { Box, Button, TextField, Typography, Container, Paper, CircularProgress, Alert, LinearProgress } from '@mui/material';
import CloudUploadIcon from '@mui/icons-material/CloudUpload';
import ImageIcon from '@mui/icons-material/Image';
import { styled } from '@mui/material/styles';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';

const VisuallyHiddenInput = styled('input')({
  clip: 'rect(0 0 0 0)',
  clipPath: 'inset(50%)',
  height: 1,
  overflow: 'hidden',
  position: 'absolute',
  bottom: 0,
  left: 0,
  whiteSpace: 'nowrap',
  width: 1,
});

const UploadStaticSitePage: React.FC = () => {
  const [file, setFile] = useState<File | null>(null);
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [title, setTitle] = useState<string>('');
  const [description, setDescription] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(false);
  const [uploadProgress, setUploadProgress] = useState<number>(0);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const navigate = useNavigate();

  const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    if (event.target.files && event.target.files[0]) {
      const selectedFile = event.target.files[0];
      if (selectedFile.type === 'application/x-zip-compressed' || selectedFile.name.endsWith('.zip')) {
        setFile(selectedFile);
        setError(null);
      } else {
        setFile(null);
        setError('ZIPファイルのみアップロードできます。');
      }
    }
  };

  const handleThumbnailChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    if (event.target.files && event.target.files[0]) {
      setThumbnail(event.target.files[0]);
    }
  };

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!file) {
      setError('ファイルを選択してください。');
      return;
    }
    if (!title.trim()) {
      setError('タイトルを入力してください。');
      return;
    }

    setLoading(true);
    setError(null);
    setSuccess(null);
    setUploadProgress(0);

    const formData = new FormData();
    formData.append('file', file);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }
    formData.append('title', title);
    formData.append('description', description);

    try {
      const token = localStorage.getItem('token');
      const response = await axios.post('/api/static-sites/upload', formData, {
        headers: {
          'Content-Type': 'multipart/form-data',
          Authorization: `Bearer ${token}`,
        },
        onUploadProgress: (progressEvent) => {
          const percentCompleted = Math.round((progressEvent.loaded * 100) / (progressEvent.total ?? 1));
          setUploadProgress(percentCompleted);
        },
      });
      setSuccess('静的サイトのアップロードが開始されました。処理が完了するまでしばらくお待ちください。');
      const { siteId, status } = response.data;
      if (siteId) {
        navigate(`/static-sites/${siteId}`, { state: { initialStatus: status } });
      } else {
        setError('アップロード後にサイトIDを取得できませんでした。詳細ページにリダイレクトできません。');
      }
    } catch (err) {
      console.error('Upload error:', err);
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || 'アップロードに失敗しました。');
      } else {
        setError('アップロード中に不明なエラーが発生しました。');
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <Container maxWidth="md" sx={{ mt: 4, mb: 4 }}>
      <Paper elevation={3} sx={{ p: 4 }}>
        <Typography variant="h4" component="h1" gutterBottom align="center">
          静的サイトをアップロード
        </Typography>
        <Typography variant="body1" color="text.secondary" align="center" sx={{ mb: 3 }}>
          HTML、CSS、JavaScriptファイルを含むZIPファイルをアップロードして、あなたのWebサイトを公開しましょう。
        </Typography>

        <Box component="form" onSubmit={handleSubmit} noValidate sx={{ mt: 1 }}>
          <TextField
            margin="normal"
            required
            fullWidth
            id="title"
            label="サイトタイトル"
            name="title"
            autoComplete="title"
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            disabled={loading}
          />
          <TextField
            margin="normal"
            fullWidth
            id="description"
            label="サイト説明"
            name="description"
            autoComplete="description"
            multiline
            rows={4}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            disabled={loading}
            sx={{ mb: 2 }}
          />

          <Button
            component="label"
            variant="contained"
            startIcon={<CloudUploadIcon />}
            fullWidth
            sx={{ mb: 2 }}
            disabled={loading}
          >
            {file ? file.name : 'ZIPファイルを選択'}
            <VisuallyHiddenInput type="file" accept=".zip,application/x-zip-compressed" onChange={handleFileChange} />
          </Button>

          <Button
            component="label"
            variant="outlined"
            startIcon={<ImageIcon />}
            fullWidth
            sx={{ mb: 2 }}
            disabled={loading}
          >
            {thumbnail ? thumbnail.name : 'サムネイル画像を選択'}
            <VisuallyHiddenInput type="file" accept="image/*" onChange={handleThumbnailChange} />
          </Button>

          {file && (
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              選択中のファイル: {file.name} ({(file.size / 1024 / 1024).toFixed(2)} MB)
            </Typography>
          )}

          {thumbnail && (
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              選択中のサムネイル: {thumbnail.name}
            </Typography>
          )}

          {loading && (
            <Box sx={{ width: '100%', mt: 2 }}>
              <LinearProgress variant="determinate" value={uploadProgress} />
              <Typography variant="body2" color="text.secondary" align="center">{`${uploadProgress}%`}</Typography>
            </Box>
          )}

          {error && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {error}
            </Alert>
          )}
          {success && (
            <Alert severity="success" sx={{ mt: 2 }}>
              {success}
            </Alert>
          )}

          <Button
            type="submit"
            fullWidth
            variant="contained"
            sx={{ mt: 3, mb: 2 }}
            disabled={loading || !file || !title.trim()}
          >
            {loading ? <CircularProgress size={24} color="inherit" /> : 'アップロード'}
          </Button>
        </Box>
      </Paper>
    </Container>
  );
};

export default UploadStaticSitePage;