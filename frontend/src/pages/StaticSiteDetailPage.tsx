import React, { useEffect, useState } from 'react';
import { useParams, useLocation, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Box, Typography, Container, Paper, CircularProgress, Alert, Button, Stack } from '@mui/material';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';
import EditIcon from '@mui/icons-material/Edit';
import { useAuth } from '../context/AuthContext';

interface StaticSite {
  id: string;
  user_id?: string;
  uploader_id?: string; // バックエンドから uploader_id で返ってくる場合にも対応
  title: string;
  description: string;
  minio_path: string;
  entry_point_path: string;
  thumbnail_url: string;
  status: string;
  processing_details: string;
  created_at: string;
}

const StaticSiteDetailPage: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const { user } = useAuth();

  const initialStatus = (location.state as { initialStatus?: string })?.initialStatus || 'unknown';

  const [staticSite, setStaticSite] = useState<StaticSite | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  const [currentStatus, setCurrentStatus] = useState<string>(initialStatus);

  const staticSiteDomain = import.meta.env.VITE_STATIC_SITE_DOMAIN || 'localhost';

  useEffect(() => {
    let pollingInterval: number;

    const fetchStaticSite = async () => {
      try {
        const response = await axios.get<StaticSite>(`/api/static-sites/${id}`);
        setStaticSite(response.data);
        setCurrentStatus(response.data.status);

        if (response.data.status === 'public' || response.data.status === 'error' || response.data.status === 'invalid') {
          setLoading(false);
          clearInterval(pollingInterval);
        }
      } catch (err) {
        console.error('Failed to fetch static site:', err);
        setError('静的サイトの読み込みに失敗しました。');
        setLoading(false);
        clearInterval(pollingInterval);
      }
    };

    if (id) {
      fetchStaticSite();
      if (currentStatus !== 'public' && currentStatus !== 'error' && currentStatus !== 'invalid') {
        pollingInterval = window.setInterval(fetchStaticSite, 5000);
      }

      return () => {
        clearInterval(pollingInterval);
      };
    } else {
      setError('サイトIDが指定されていません。');
      setLoading(false);
    }
  }, [id, currentStatus]);

  const handleEditClick = () => {
    navigate(`/edit-static-site/${id}`);
  };

  if (loading || (staticSite && (staticSite.status === 'scanning' || staticSite.status === 'processing'))) {
    let message = 'サイトを準備中...';
    if (staticSite?.status === 'scanning') {
      message = 'セキュリティスキャンを実行中...';
    } else if (staticSite?.status === 'processing') {
      message = staticSite?.processing_details || 'サイトファイルを準備中...';
    }
    return (
        <Container maxWidth="md" sx={{ mt: 4, mb: 4, textAlign: 'center' }}>
          <CircularProgress />
          <Typography variant="h6" sx={{ mt: 2 }}>{message}</Typography>
          <Typography variant="body2" color="text.secondary">
            しばらくお待ちください。
          </Typography>
        </Container>
    );
  }

  if (error) {
    return (
        <Container maxWidth="md" sx={{ mt: 4, mb: 4 }}>
          <Alert severity="error">{error}</Alert>
        </Container>
    );
  }

  if (!staticSite) {
    return (
        <Container maxWidth="md" sx={{ mt: 4, mb: 4 }}>
          <Alert severity="info">静的サイトが見つかりませんでした。</Alert>
        </Container>
    );
  }

  const publicSiteUrl = staticSite.status === 'public'
      ? `http://${staticSite.id}.${staticSiteDomain}:3001/${staticSite.entry_point_path}`
      : '';

  // ★ 修正: 柔軟なID判定ロジック (表記揺れ・大文字小文字の差異をカバー)
  const currentUserId = user
      ? ((user as any).user_id || (user as any).id || (user as any).userId || (user as any).userID || (user as any).sub)
      : null;

  const siteOwnerId = staticSite
      ? (staticSite.user_id || staticSite.uploader_id || (staticSite as any).uploaderId)
      : null;

  const isOwner = Boolean(
      currentUserId &&
      siteOwnerId &&
      String(currentUserId).toLowerCase().trim() === String(siteOwnerId).toLowerCase().trim()
  );

  return (
      <Container maxWidth="lg" sx={{ mt: 4, mb: 4 }}>
        <Paper elevation={3} sx={{ p: 4, mb: 4 }}>
          <Typography variant="h4" component="h1" gutterBottom>
            {staticSite.title}
          </Typography>
          <Typography variant="body1" color="text.secondary" paragraph>
            {staticSite.description || '説明はありません。'}
          </Typography>
          <Typography variant="caption" color="text.disabled">
            アップロード日時: {new Date(staticSite.created_at).toLocaleString()}
          </Typography>

          {staticSite.status === 'public' && publicSiteUrl && (
              <Stack direction="row" spacing={2} sx={{ mt: 3 }}>
                <Button
                    variant="contained"
                    color="primary"
                    startIcon={<OpenInNewIcon />}
                    href={publicSiteUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                >
                  新しいタブで開く
                </Button>
                {isOwner && (
                    <Button
                        variant="outlined"
                        color="secondary"
                        startIcon={<EditIcon />}
                        onClick={handleEditClick}
                    >
                      編集
                    </Button>
                )}
              </Stack>
          )}

          {staticSite.status === 'error' && (
              <Alert severity="error" sx={{ mt: 3 }}>
                サイトの処理中にエラーが発生しました: {staticSite.processing_details}
              </Alert>
          )}
        </Paper>

        {staticSite.status === 'public' && publicSiteUrl && (
            <Paper elevation={3} sx={{ p: 2, height: '80vh', overflow: 'hidden' }}>
              <Typography variant="h6" gutterBottom>
                プレビュー (クリックでサイトを開く)
              </Typography>

              <Box
                  onClick={() => window.open(publicSiteUrl, '_blank', 'noopener,noreferrer')}
                  sx={{
                    position: 'relative',
                    width: '100%',
                    height: '100%',
                    cursor: 'pointer',
                    borderRadius: 1,
                    overflow: 'hidden',
                    '&:hover .iframe-overlay': {
                      backgroundColor: 'rgba(0, 0, 0, 0.08)',
                    },
                    '&:hover .launch-badge': {
                      opacity: 1,
                    },
                  }}
              >
                <iframe
                    src={publicSiteUrl}
                    title={staticSite.title}
                    style={{
                      width: '100%',
                      height: '100%',
                      border: 'none',
                      pointerEvents: 'none',
                    }}
                    sandbox="allow-scripts allow-forms allow-popups allow-modals allow-pointer-lock allow-same-origin"
                />

                <Box
                    className="iframe-overlay"
                    sx={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      width: '100%',
                      height: '100%',
                      transition: 'background-color 0.2s ease',
                    }}
                />

                <Box
                    className="launch-badge"
                    sx={{
                      position: 'absolute',
                      top: '50%',
                      left: '50%',
                      transform: 'translate(-50%, -50%)',
                      opacity: 0,
                      transition: 'opacity 0.2s ease',
                      backgroundColor: 'rgba(0, 0, 0, 0.75)',
                      color: '#fff',
                      px: 3,
                      py: 1.5,
                      borderRadius: 2,
                      display: 'flex',
                      alignItems: 'center',
                      gap: 1,
                      boxShadow: 3,
                      pointerEvents: 'none',
                    }}
                >
                  <OpenInNewIcon fontSize="small" />
                  <Typography variant="subtitle2" fontWeight="bold">
                    クリックして別タブで表示
                  </Typography>
                </Box>
              </Box>
            </Paper>
        )}
      </Container>
  );
};

export default StaticSiteDetailPage;