import { useState, useEffect, useCallback } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Container,
  Typography,
  Box,
  Paper,
  TextField,
  Button,
  Alert,
  CircularProgress,
  useTheme,
  Divider,
  Avatar,
  Chip,
  IconButton,
  Tooltip,
  Tabs,
  Tab,
  Grid,
  Card,
  CardMedia,
  CardContent,
  CardActions,
  InputLabel,
  Accordion,
  AccordionSummary,
  AccordionDetails,
} from '@mui/material';
import MenuItem from '@mui/material/MenuItem';
import AddIcon from '@mui/icons-material/Add';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import DeleteIcon from '@mui/icons-material/Delete';
import EditIcon from '@mui/icons-material/Edit';
import SearchIcon from '@mui/icons-material/Search';
import VideoIcon from '@mui/icons-material/PlayArrow';
import GamesIcon from '@mui/icons-material/SportsEsports';
import WebIcon from '@mui/icons-material/Web';
import LockIcon from '@mui/icons-material/Lock';
import PublicIcon from '@mui/icons-material/Public';
import axios from 'axios';
import {
  teamApi,
  Team,
  TeamMember,
  TEAM_ROLE_RANK,
  contentApi,
  TeamContentItem,
  TeamContentType,
  CONTENT_LINKS,
  CONTENT_UPLOAD_FIELDS,
} from '../api/team';
import { useAuth } from '../context/AuthContext';

const ROLE_LABEL: Record<string, string> = { owner: '所有者', admin: '管理者', member: 'メンバー' };

const TABS: { key: string; label: string }[] = [
  { key: 'all', label: 'すべて' },
  { key: 'videos', label: '動画' },
  { key: 'games', label: 'ゲーム' },
  { key: 'sites', label: 'サイト' },
  { key: 'members', label: 'メンバー' },
];

const CONTENT_TAB_KEYS: TeamContentType[] = ['videos', 'games', 'sites'];

// サムネを強制的に取得するためのキャッシュバスタ（アップロード後に旧画像が混入するのを防止）
const withCacheBuster = (url: string | null | undefined): string | undefined => {
  if (!url) return undefined;
  const sep = url.includes('?') ? '&' : '?';
  return `${url}${sep}v=${encodeURIComponent(url)}`;
};

const TeamDetailPage = () => {
  const { token } = useParams<{ token: string }>();
  const { user, isAuthenticated } = useAuth();
  const navigate = useNavigate();
  const theme = useTheme();

  const [team, setTeam] = useState<Team | null>(null);
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  // アクティブタブ
  const [activeTab, setActiveTab] = useState('posts');

// メンバー
  const [memberUserId, setMemberUserId] = useState('');
  const [memberRole, setMemberRole] = useState('member');
  const [memberError, setMemberError] = useState('');

  // コンテンツ（動画/ゲーム/サイト）
  const [contentSearch, setContentSearch] = useState<Record<TeamContentType, string>>({
    videos: '',
    games: '',
    sites: '',
  });
  const [videos, setVideos] = useState<TeamContentItem[]>([]);
  const [games, setGames] = useState<TeamContentItem[]>([]);
  const [staticSites, setStaticSites] = useState<TeamContentItem[]>([]);
  const [contentLoading, setContentLoading] = useState(false);
  const [contentError, setContentError] = useState('');

  // アップロード状態
  const [uploading, setUploading] = useState<Record<TeamContentType, boolean>>({
    videos: false,
    games: false,
    sites: false,
  });
  const [uploadForm, setUploadForm] = useState<Record<TeamContentType, { title: string; description: string; file: File | null; thumbnail: File | null }>>({
    videos: { title: '', description: '', file: null, thumbnail: null },
    games: { title: '', description: '', file: null, thumbnail: null },
    sites: { title: '', description: '', file: null, thumbnail: null },
  });
  const [uploadError, setUploadError] = useState<Record<TeamContentType, string>>({ videos: '', games: '', sites: '' });
  const [uploadSuccess, setUploadSuccess] = useState<Record<TeamContentType, string>>({ videos: '', games: '', sites: '' });

  const myRole = (() => {
    if (!user) return '';
    const m = members.find((x) => x.user_id === user.userID);
    return m ? m.role : '';
  })();
  const canPost = !!myRole && TEAM_ROLE_RANK[myRole] >= TEAM_ROLE_RANK.member;
  const canManageMembers = !!myRole && TEAM_ROLE_RANK[myRole] >= TEAM_ROLE_RANK.admin;
  const canManageTeam = !!myRole && TEAM_ROLE_RANK[myRole] >= TEAM_ROLE_RANK.owner;

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const teamRes = await teamApi.get(token!);
      setTeam(teamRes.data);

      // 非公開チームでメンバー以外（或未認証）は閲覧不可
      if (!teamRes.data.is_public) {
        if (!isAuthenticated || !myRole) {
          setError('このチームは非公開です。メンバーがログインしてください。');
          setLoading(false);
          return;
        }
      }

      await teamApi.listMembers(token!).then((r) => setMembers(r.data || [])).catch(() => {});
    } catch (err: any) {
      if (err.response?.status === 401) {
        setError('このチームを閲覧するにはログインが必要です。');
      } else if (err.response?.status === 403) {
        setError('このチームを閲覧する権限がありません。');
      } else if (err.response?.status === 404) {
        setError('チームが見つかりません。');
      } else {
        setError('チームを取得できませんでした。');
      }
    } finally {
      setLoading(false);
    }
  }, [token, isAuthenticated, myRole]);

  useEffect(() => {
    load();
  }, [load]);

  // コンテンツ一覧取得
  const loadContent = useCallback(
    async (type: TeamContentType) => {
      if (!token) return;
      setContentLoading(true);
      setContentError('');
      try {
        const search = contentSearch[type];
        if (type === 'videos') {
          const r = await contentApi.listVideos(token, search);
          setVideos(r.data || []);
        } else if (type === 'games') {
          const r = await contentApi.listGames(token, search);
          setGames(r.data || []);
        } else {
          const r = await contentApi.listStaticSites(token, search);
          setStaticSites(r.data || []);
        }
      } catch (err: any) {
        setContentError('一覧の取得に失敗しました。');
      } finally {
        setContentLoading(false);
      }
    },
    [token, contentSearch]
  );

  useEffect(() => {
    if (activeTab === 'all') {
      Promise.all([
        loadContent('videos'),
        loadContent('games'),
        loadContent('sites'),
      ]);
    } else if (CONTENT_TAB_KEYS.includes(activeTab as TeamContentType)) {
      loadContent(activeTab as TeamContentType);
    }
  }, [activeTab, loadContent]);

  const handleAddMember = async () => {
    if (!memberUserId.trim()) {
      setMemberError('ユーザーIDを入力してください。');
      return;
    }
    setMemberError('');
    try {
      await teamApi.addMember(token!, memberUserId.trim(), memberRole);
      await load();
      setMemberUserId('');
    } catch (err: any) {
      setMemberError(err.response?.data?.error || 'メンバー追加に失敗しました。');
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (window.confirm('このメンバーを削除しますか？')) {
      try {
        await teamApi.removeMember(token!, userId);
        await load();
      } catch (err) {
        console.error(err);
        setError('メンバー削除に失敗しました。');
      }
    }
  };

  const handleDeleteTeam = () => {
    if (window.confirm('このチームを削除しますか？関連する投稿もすべて削除されます。')) {
      teamApi.deleteTeam(token!).then(() => navigate('/teams')).catch(() => setError('チーム削除に失敗しました。'));
    }
  };

  // コンテンツアップロード
  const handleUpload = async (type: TeamContentType) => {
    const form = uploadForm[type];
    if (!form.title.trim()) {
      setUploadError((prev) => ({ ...prev, [type]: 'タイトルを入力してください。' }));
      return;
    }
    if (!form.file) {
      setUploadError((prev) => ({ ...prev, [type]: 'ファイルを選択してください。' }));
      return;
    }
    setUploading((prev) => ({ ...prev, [type]: true }));
    setUploadError((prev) => ({ ...prev, [type]: '' }));
    setUploadSuccess((prev) => ({ ...prev, [type]: '' }));

    try {
      const fields = CONTENT_UPLOAD_FIELDS[type];
      const formData = new FormData();
      formData.append('title', form.title.trim());
      formData.append('description', form.description);
      formData.append('team_id', token!);
      formData.append(fields.fileField, form.file);
      if (form.thumbnail) {
        formData.append('thumbnail', form.thumbnail);
      }

      const res = await axios.post(fields.endpoint, formData);
      setUploadSuccess((prev) => ({ ...prev, [type]: res.data.message || 'アップロードが開始されました。処理が完了するまでしばらくお待ちください。' }));
      setUploadForm((prev) => ({ ...prev, [type]: { title: '', description: '', file: null, thumbnail: null } }));
      await loadContent(type);
    } catch (err: any) {
      setUploadError((prev) => ({ ...prev, [type]: err.response?.data?.error || 'アップロードに失敗しました。' }));
    } finally {
      setUploading((prev) => ({ ...prev, [type]: false }));
    }
  };

  // コンテンツ削除
  const handleDeleteContent = async (type: TeamContentType, id: string) => {
    if (!window.confirm('このコンテンツを削除しますか？')) return;
    try {
      if (type === 'videos') {
        await contentApi.deleteVideo(id);
      } else if (type === 'games') {
        await contentApi.deleteGame(id);
      } else {
        await contentApi.deleteStaticSite(id);
      }
      await loadContent(type);
    } catch (err: any) {
      setError('削除に失敗しました。');
    }
  };

  const setUploadField = (type: TeamContentType, field: 'title' | 'description', value: string) => {
    setUploadForm((prev) => ({ ...prev, [type]: { ...prev[type], [field]: value } }));
  };
  const setUploadFile = (type: TeamContentType, file: File | null, field: 'file' | 'thumbnail') => {
    setUploadForm((prev) => ({ ...prev, [type]: { ...prev[type], [field]: file } }));
  };

  const renderContentThumbnail = (item: TeamContentItem) => {
    const thumb = item.thumbnail_path || item.thumbnail_url;
    if (thumb) {
      return <CardMedia component="img" height="140" image={withCacheBuster(thumb)} alt={item.title} />;
    }
    return (
      <Box sx={{ height: 140, display: 'flex', alignItems: 'center', justifyContent: 'center', bgcolor: 'action.hover' }}>
        <Typography color="text.secondary">サムネイルなし</Typography>
      </Box>
    );
  };

  const renderContentGrid = (type: TeamContentType, items: TeamContentItem[]) => {
    const link = CONTENT_LINKS[type];
    const icons = { videos: <VideoIcon />, games: <GamesIcon />, sites: <WebIcon /> };
    return (
      <Grid container spacing={3}>
        {items.map((item) => (
          <Grid item key={`${type}-${item.id}`} xs={12} sm={6} md={4} lg={3}>
            <Card
                component="a"
                href={link.detail(item.id)}
                sx={{
                  display: 'flex',
                  flexDirection: 'column',
                  height: '100%',
                  textDecoration: 'none',
                  color: 'inherit',
                  border: `1px solid ${theme.palette.divider}`,
                  borderRadius: 1,
                  '&:hover': { boxShadow: theme.shadows[4] },
                  cursor: 'pointer',
                }}
              >
                {renderContentThumbnail(item)}
                <CardContent sx={{ flexGrow: 1 }}>
                  <Typography variant="h6" component="div" noWrap title={item.title}>
                    {item.title}
                  </Typography>
                  <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: 60, overflow: 'hidden' }}>
                    {item.description || '説明はありません。'}
                  </Typography>
                  <Chip size="small" label={item.status} sx={{ mt: 1 }} color={item.status === 'public' ? 'success' : 'default'} />
                </CardContent>
                <CardActions sx={{ mt: 'auto' }}>
                  <Button size="small" onClick={(e) => e.stopPropagation()} startIcon={icons[type]}>
                    詳細
                  </Button>
                  <Button size="small" onClick={(e) => e.stopPropagation()} startIcon={<EditIcon />}>
                    編集
                  </Button>
                  {canManageTeam && (
                    <Button
                      size="small"
                      color="error"
                      startIcon={<DeleteIcon />}
                      onClick={(e) => {
                        e.stopPropagation();
                        handleDeleteContent(type, item.id);
                      }}
                    >
                      削除
                    </Button>
                  )}
                </CardActions>
              </Card>
          </Grid>
        ))}
      </Grid>
    );
  };

  const renderUploadForm = (type: TeamContentType, label: string, accept: string, hint: string) => {
    const form = uploadForm[type];
    const error = uploadError[type];
    const success = uploadSuccess[type];
    return (
      <Accordion variant="outlined" sx={{ mb: 3 }}>
        <AccordionSummary expandIcon={<ExpandMoreIcon />}>
          <Typography sx={{ fontWeight: 600 }}>
            <AddIcon sx={{ mr: 1, verticalAlign: 'middle' }} />
            {label}をアップロード
          </Typography>
        </AccordionSummary>
        <AccordionDetails sx={{ display: 'flex', flexDirection: 'column' }}>
          {error && <Alert severity="error" sx={{ mb: 1 }}>{error}</Alert>}
          {success && <Alert severity="success" sx={{ mb: 1 }}>{success}</Alert>}
          <TextField
            fullWidth
            margin="normal"
            label="タイトル"
            required
            value={form.title}
            onChange={(e) => setUploadField(type, 'title', e.target.value)}
          />
          <TextField
            fullWidth
            margin="normal"
            label="説明"
            multiline
            minRows={3}
            value={form.description}
            onChange={(e) => setUploadField(type, 'description', e.target.value)}
          />
          <Box sx={{ mt: 1 }}>
            <InputLabel>ファイル</InputLabel>
            <Button variant="outlined" component="label" sx={{ mt: 0.5 }}>
              {form.file ? form.file.name : 'ファイルを選択'}
              <input type="file" hidden accept={accept} onChange={(e) => setUploadFile(type, e.target.files?.[0] ?? null, 'file')} />
            </Button>
            {form.file && (
              <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
                {form.file.name} ({(form.file.size / 1024 / 1024).toFixed(2)} MB)
              </Typography>
            )}
            <Typography variant="caption" color="text.secondary">{hint}</Typography>
          </Box>
          <Box sx={{ mt: 1 }}>
            <InputLabel>サムネイル画像（任意）</InputLabel>
            <Button variant="text" component="label" sx={{ mt: 0.5 }}>
              {form.thumbnail ? form.thumbnail.name : 'サムネイルを選択'}
              <input type="file" hidden accept="image/*" onChange={(e) => setUploadFile(type, e.target.files?.[0] ?? null, 'thumbnail')} />
            </Button>
          </Box>
          <Button
            variant="contained"
            onClick={() => handleUpload(type)}
            disabled={uploading[type] || !form.file || !form.title.trim()}
            sx={{ mt: 2 }}
            startIcon={uploading[type] ? <CircularProgress size={20} /> : <AddIcon />}
          >
            {uploading[type] ? 'アップロード中' : 'アップロード'}
          </Button>
        </AccordionDetails>
      </Accordion>
    );
  };

  if (loading) {
    return (
      <Container maxWidth="sm" sx={{ py: 8, display: 'flex', justifyContent: 'center' }}>
        <CircularProgress />
      </Container>
    );
  }

  return (
    <Container maxWidth="lg" sx={{ py: 6 }}>
      {error && (
        <Alert severity={error.includes('権限') ? 'warning' : 'error'} sx={{ mb: 2 }}>
          {error.includes('権限') && <LockIcon sx={{ mr: 1, verticalAlign: 'middle' }} />}
          {error}
        </Alert>
      )}

      {team && !error && (
        <>
          <Paper sx={{ p: 3, mb: 3, borderTop: `4px solid ${theme.palette.primary.main}` }}>
            <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between' }}>
              <Box>
                <Typography variant="h4" component="h1" fontWeight="bold">{team.name}</Typography>
                <Box sx={{ display: 'flex', alignItems: 'center', mt: 1, mb: 1 }}>
                  <Chip size="small" icon={team.is_public ? <PublicIcon /> : <LockIcon />} label={team.is_public ? '公開チーム' : '非公開チーム'} sx={{ ml: 1 }} />
                </Box>
                {team.description && (
                  <Typography color="text.secondary" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                    {team.description}
                  </Typography>
                )}
              </Box>
              {canManageTeam && (
                <Button size="small" onClick={handleDeleteTeam} color="error">
                  チームを削除
                </Button>
              )}
            </Box>
          </Paper>

          <Paper sx={{ p: 1, mb: 2 }}>
            <Tabs
              value={activeTab}
              onChange={(_e, v) => setActiveTab(v)}
              variant="scrollable"
              scrollButtons="auto"
              sx={{ borderBottom: 1, borderColor: 'divider' }}
            >
              {TABS.map((t) => (
                <Tab key={t.key} value={t.key} label={t.label} />
              ))}
            </Tabs>
          </Paper>

{/* すべて */}
           {activeTab === 'all' && (
             <>
               {contentLoading ? (
                 <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
               ) : contentError ? (
                 <Alert severity="error">{contentError}</Alert>
               ) : (videos.length + games.length + staticSites.length) === 0 ? (
                 <Paper sx={{ p: 4, textAlign: 'center' }}>
                   <Typography color="text.secondary">まだ投稿物がありません。</Typography>
                 </Paper>
               ) : (
                 <>
                   {videos.length > 0 && (
                     <Box sx={{ mb: 4 }}>
                       <Typography variant="h6" gutterBottom sx={{ display: 'flex', alignItems: 'center' }}>
                         <VideoIcon sx={{ mr: 1 }} /> 動画 ({videos.length})
                       </Typography>
                       {renderContentGrid('videos', videos)}
                     </Box>
                   )}
                   {games.length > 0 && (
                     <Box sx={{ mb: 4 }}>
                       <Typography variant="h6" gutterBottom sx={{ display: 'flex', alignItems: 'center' }}>
                         <GamesIcon sx={{ mr: 1 }} /> ゲーム ({games.length})
                       </Typography>
                       {renderContentGrid('games', games)}
                     </Box>
                   )}
                   {staticSites.length > 0 && (
                     <Box sx={{ mb: 4 }}>
                       <Typography variant="h6" gutterBottom sx={{ display: 'flex', alignItems: 'center' }}>
                         <WebIcon sx={{ mr: 1 }} /> サイト ({staticSites.length})
                       </Typography>
                       {renderContentGrid('sites', staticSites)}
                     </Box>
                   )}
                 </>
               )}
             </>
           )}

          {/* 動画 */}
          {activeTab === 'videos' && (
            <>
              {canPost && (
                <>
                  <Box sx={{ mb: 2 }}>
                    <TextField
                      fullWidth
                      size="small"
                      placeholder="動画を検索..."
                      value={contentSearch.videos}
                      onChange={(e) => setContentSearch((prev) => ({ ...prev, videos: e.target.value }))}
                      InputProps={{ startAdornment: <SearchIcon sx={{ mr: 1, color: 'text.secondary' }} />, endAdornment: <></> }}
                    />
                  </Box>
                  {renderUploadForm('videos', '動画', 'video/*,video/webm', '動画ファイルを選択してください。')}
                </>
              )}
              {contentLoading ? (
                <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
              ) : contentError ? (
                <Alert severity="error">{contentError}</Alert>
              ) : videos.length === 0 ? (
                <Paper sx={{ p: 4, textAlign: 'center' }}>
                  <Typography color="text.secondary">まだ動画がありません。</Typography>
                </Paper>
              ) : (
                renderContentGrid('videos', videos)
              )}
            </>
          )}

          {/* ゲーム */}
          {activeTab === 'games' && (
            <>
              {canPost && (
                <Box sx={{ mb: 2 }}>
                  <TextField
                    fullWidth
                    size="small"
                    placeholder="ゲームを検索..."
                    value={contentSearch.games}
                    onChange={(e) => setContentSearch((prev) => ({ ...prev, games: e.target.value }))}
                    InputProps={{ startAdornment: <SearchIcon sx={{ mr: 1, color: 'text.secondary' }} /> }}
                  />
                </Box>
              )}
              {renderUploadForm('games', 'ゲーム', '.zip,application/x-zip-compressed', 'ZIPファイル（Unity WebGL版）を選択してください。')}
              {contentLoading ? (
                <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
              ) : contentError ? (
                <Alert severity="error">{contentError}</Alert>
              ) : games.length === 0 ? (
                <Paper sx={{ p: 4, textAlign: 'center' }}>
                  <Typography color="text.secondary">まだゲームがありません。</Typography>
                </Paper>
              ) : (
                renderContentGrid('games', games)
              )}
            </>
          )}

          {/* 静的サイト */}
          {activeTab === 'sites' && (
            <>
              {canPost && (
                <Box sx={{ mb: 2 }}>
                  <TextField
                    fullWidth
                    size="small"
                    placeholder="サイトを検索..."
                    value={contentSearch.sites}
                    onChange={(e) => setContentSearch((prev) => ({ ...prev, sites: e.target.value }))}
                    InputProps={{ startAdornment: <SearchIcon sx={{ mr: 1, color: 'text.secondary' }} /> }}
                  />
                </Box>
              )}
              {renderUploadForm('sites', '静的サイト', '.zip,application/x-zip-compressed', 'HTML/CSS/JSファイルを含むZIPファイルを選択してください。')}
              {contentLoading ? (
                <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }}><CircularProgress /></Box>
              ) : contentError ? (
                <Alert severity="error">{contentError}</Alert>
              ) : staticSites.length === 0 ? (
                <Paper sx={{ p: 4, textAlign: 'center' }}>
                  <Typography color="text.secondary">まだサイトがありません。</Typography>
                </Paper>
              ) : (
                renderContentGrid('sites', staticSites)
              )}
            </>
          )}

          {/* メンバー */}
          {activeTab === 'members' && (
            <>
              {canManageMembers && (
                <>
                  <Typography variant="h6" gutterBottom>メンバー管理</Typography>
                  {members.length === 0 ? (
                    <Alert severity="info">メンバーがいません。</Alert>
                  ) : (
                    members.map((m) => (
                      <Box key={`${m.user_id}-${m.role}`} sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', py: 1 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center' }}>
                          <Avatar sx={{ width: 32, height: 32, mr: 1 }}>{(m.user_id || '?').charAt(0).toUpperCase()}</Avatar>
                          <Typography variant="body2">{m.user_id}</Typography>
                          <Chip size="small" label={ROLE_LABEL[m.role] || m.role} sx={{ ml: 1 }} />
                        </Box>
                        {canManageTeam && m.role !== 'owner' && (
                          <Tooltip title="メンバーを削除">
                            <IconButton onClick={() => handleRemoveMember(m.user_id)} size="small">
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    ))
                  )}

                  <Divider sx={{ my: 2 }} />
                  {memberError && <Alert severity="error" sx={{ mb: 1 }}>{memberError}</Alert>}
                  <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
                    <TextField label="追加ユーザーID" value={memberUserId} onChange={(e) => setMemberUserId(e.target.value)} size="small" />
                    <Box sx={{ display: 'flex', gap: 1 }}>
                      <TextField
                        select
                        size="small"
                        value={memberRole}
                        onChange={(e) => setMemberRole(e.target.value)}
                        sx={{ maxWidth: 200 }}
                      >
                        <MenuItem value="member">メンバー</MenuItem>
                        <MenuItem value="admin">管理者</MenuItem>
                        <MenuItem value="owner">所有者</MenuItem>
                      </TextField>
                      <Button variant="outlined" onClick={handleAddMember} sx={{ flexShrink: 0 }}>
                        メンバーを追加
                      </Button>
                    </Box>
                  </Box>
                </>
              )}
              {!canManageMembers && (
                <Alert severity="info">メンバー管理には管理者権限が必要です。</Alert>
              )}
            </>
          )}
        </>
      )}

      <Box sx={{ mt: 3 }}>
        <Button onClick={() => navigate('/teams')}>← チーム一覧に戻る</Button>
      </Box>
    </Container>
  );
};

export default TeamDetailPage;