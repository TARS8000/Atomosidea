import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

const MAX_POLL_MS = 120000;

const EditStaticSitePage = () => {
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
    const fetchSite = async () => {
      try {
        const response = await axios.get(`/api/static-sites/${id}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        setTitle(response.data.title);
        setDescription(response.data.description);
        setExistingThumbnailUrl(response.data.thumbnail_url);
        setThumbnailUrl(response.data.thumbnail_url);
      } catch (err) {
        setError('サイト情報の取得に失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchSite();
  }, [id, token]);

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      setThumbnail(file);
      setThumbnailUrl(URL.createObjectURL(file));
    }
  };

  const pollStaticSite = async (signal: AbortSignal): Promise<{ thumbnailUrl: string; accepted: boolean }> => {
    return new Promise((resolve, reject) => {
      const startTime = Date.now();
      const pollInterval = setInterval(async () => {
        try {
          const siteRes = await axios.get(`/api/static-sites/${id}`, { signal });
          const site = siteRes.data;
          if (!site.thumbnail_sfsp_job_id) {
            clearInterval(pollInterval);
            // The job_id is cleared when the scan finishes. A new thumbnail is
            // only accepted when the stored URL changed from the one we started
            // with (the worker writes a fresh object on success, leaving the old
            // URL untouched when the scan rejects the file).
            const storedUrl = typeof site.thumbnail_url === 'string' ? site.thumbnail_url : '';
            const accepted = storedUrl !== existingThumbnailUrl;
            resolve({
              thumbnailUrl: accepted ? storedUrl : existingThumbnailUrl,
              accepted,
            });
          }
        } catch (err) {
          if (!axios.isCancel(err)) {
            console.error('Polling static site failed:', err);
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

    try {
      if (thumbnail) {
        // Step 1: upload thumbnail only (triggers SFSP scan). Title/description
        // are deferred until the scan completes so the site is not half-updated
        // if the thumbnail is rejected.
        setIsScanningThumbnail(true);
        const thumbFormData = new FormData();
        thumbFormData.append('thumbnail', thumbnail);
        const res = await axios.put(`/api/static-sites/${id}`, thumbFormData, {
          headers: {
            'Content-Type': 'multipart/form-data',
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          // Wait for the SFSP scan to finish. The worker writes the clean
          // thumbnail to storage and clears the job id; the stored URL then
          // differs from the one we started with.
          const result = await pollStaticSite(controller.signal);
          if (!result.accepted) {
            // Rejected: keep the previous thumbnail and surface the error.
            setIsScanningThumbnail(false);
            setThumbnailUrl(existingThumbnailUrl);
            setError('サムネイルがセキュリティスキャンで拒否されました。別の画像を選択してください。');
            return;
          }
          // Accepted: show the freshly written thumbnail from storage.
          setThumbnailUrl(result.thumbnailUrl);
        } else if (res.data.thumbnail_url) {
          // SFSP returned immediately (e.g. a deduplicated clean file).
          setThumbnailUrl(res.data.thumbnail_url);
        }

        // Step 2: scan finished (or was skipped). Commit title/description.
        const infoFormData = new FormData();
        infoFormData.append('title', title);
        infoFormData.append('description', description);
        await axios.put(`/api/static-sites/${id}`, infoFormData, {
          headers: {
            'Content-Type': 'multipart/form-data',
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        setSuccess('更新が完了しました。');
      } else {
        // No thumbnail uploaded: just update title/description.
        const formData = new FormData();
        formData.append('title', title);
        formData.append('description', description);
        await axios.put(`/api/static-sites/${id}`, formData, {
          headers: {
            'Content-Type': 'multipart/form-data',
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        setSuccess('サイト情報が更新されました。');
      }

      // Navigate to the detail page once the UI has settled (short delay keeps
      // any success message visible). The preview already points at the
      // server-side thumbnail, so no client-side preloading is required.
      setTimeout(() => navigate(`/static-sites/${id}`), 1500);
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
      await axios.delete(`/api/static-sites/${id}/thumbnail`, {
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
        静的サイト情報を編集
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

export default EditStaticSitePage;
