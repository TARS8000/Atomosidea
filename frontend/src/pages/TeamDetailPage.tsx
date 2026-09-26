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
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  FormControlLabel,
  Switch,
} from '@mui/material';
import MenuItem from '@mui/material/MenuItem';
import DeleteIcon from '@mui/icons-material/Delete';
import EditIcon from '@mui/icons-material/Edit';
import GroupIcon from '@mui/icons-material/Group';
import SearchIcon from '@mui/icons-material/Search';
import VideoIcon from '@mui/icons-material/PlayArrow';
import GamesIcon from '@mui/icons-material/SportsEsports';
import WebIcon from '@mui/icons-material/Web';
import LockIcon from '@mui/icons-material/Lock';
import PublicIcon from '@mui/icons-material/Public';
import PersonIcon from '@mui/icons-material/Person';
import {
  teamApi,
  Team,
  TeamMember,
  JoinRequest,
  TEAM_ROLE_RANK,
  contentApi,
  TeamContentItem,
  TeamContentType,
  CONTENT_LINKS,
} from '../api/team';
import { useAuth } from '../context/AuthContext';
import axios from 'axios';

const ROLE_LABEL: Record<string, string> = { owner: 'ホスト', admin: 'オペレーター', member: 'メンバー' };

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
  const [activeTab, setActiveTab] = useState('all');

// メンバー

  // チーム設定
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [autoApprove, setAutoApprove] = useState(false);
  const [isPublic, setIsPublic] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);
  const [settingsMessage, setSettingsMessage] = useState('');
  const [teamName, setTeamName] = useState('');
  const [teamDescription, setTeamDescription] = useState('');
  const [allowMemberInvite, setAllowMemberInvite] = useState(false);

  // 参加リクエスト管理
  const [joinRequests, setJoinRequests] = useState<JoinRequest[]>([]);
  const [joinRequestsLoading, setJoinRequestsLoading] = useState(false);
  const [joinMessage, setJoinMessage] = useState('');

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
  const [usernames, setUsernames] = useState<Record<string, string>>({});

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

      // メンバー一覧を先に取得する。非公開チームのアクセス判定は myRole に依存するため、
      // members が空の段階で判定するとメンバー（ホスト含む）でも閲覧不可になってしまう。
      let role = '';
      await teamApi.listMembers(token!).then((r) => {
        const list = r.data || [];
        setMembers(list);
        const m = list.find((x) => x.user_id === user?.userID);
        role = m ? m.role : '';
      }).catch(() => {});

      // 非公開チームでメンバー以外（或未認証）は閲覧不可
      if (!teamRes.data.is_public) {
        if (!isAuthenticated || !role) {
          setError('このチームは非公開です。メンバーがログインしてください。');
          setLoading(false);
          return;
        }
      }
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
  }, [token, isAuthenticated, user?.userID]);

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
          void loadUsernames(r.data || []);
        } else if (type === 'games') {
          const r = await contentApi.listGames(token, search);
          setGames(r.data || []);
          void loadUsernames(r.data || []);
        } else {
          const r = await contentApi.listStaticSites(token, search);
          setStaticSites(r.data || []);
          void loadUsernames(r.data || []);
        }
      } catch (err: any) {
        setContentError('一覧の取得に失敗しました。');
      } finally {
        setContentLoading(false);
      }
    },
    [token, contentSearch]
  );

  // 投稿者の名前を一括取得（詳細ページと同じく /api/profile/:id を使用）
  const loadUsernames = useCallback(async (items: Array<{ uploader_id?: string | null; user_id?: string | null }>) => {
    const ids = Array.from(new Set(items.map((it) => it.uploader_id ?? it.user_id).filter(Boolean) as string[]));
    if (ids.length === 0) return;
    try {
      const resps = await Promise.all(
        ids.map((id) => axios.get<{ username: string }>(`/api/profile/${id}`).catch(() => ({ data: { username: '' } })))
      );
      const map: Record<string, string> = {};
      resps.forEach((r, i) => {
        const uname = r.data?.username?.trim();
        if (uname) map[ids[i]] = uname;
      });
      setUsernames((prev) => ({ ...prev, ...map }));
    } catch {
      // 名前取得失敗時はカード側でuploader_idを表示
    }
  }, []);

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

  // メンバーと参加リクエストの名前を取得
  useEffect(() => {
    if (members.length > 0) void loadUsernames(members);
  }, [members]);

  useEffect(() => {
    if (joinRequests.length > 0) void loadUsernames(joinRequests);
  }, [joinRequests]);

  const handleRoleChange = async (userId: string, role: string) => {
    try {
      await teamApi.addMember(token!, userId, role);
      await load();
    } catch (err: any) {
      setError(err.response?.data?.error || '権限変更に失敗しました。');
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

  // 参加リクエスト一覧の取得
  const loadJoinRequests = async () => {
    setJoinRequestsLoading(true);
    try {
      const r = await teamApi.listJoinRequests(token!);
      setJoinRequests(r.data || []);
    } catch (err) {
      console.error(err);
    } finally {
      setJoinRequestsLoading(false);
    }
  };

  const openSettings = () => {
    if (!team) return;
    setTeamName(team.name);
    setTeamDescription(team.description || '');
    setAutoApprove(team.auto_approve);
    setIsPublic(team.is_public);
    setAllowMemberInvite(!!team.allow_member_invite);
    setSettingsMessage('');
    setSettingsOpen(true);
  };

  const saveSettings = async () => {
    setSavingSettings(true);
    setSettingsMessage('');
    try {
      await teamApi.update(token!, { name: teamName, description: teamDescription, is_public: isPublic, auto_approve: autoApprove, allow_member_invite: allowMemberInvite });
      setSettingsMessage('設定を保存しました。');
      await load();
    } catch (err: any) {
      setSettingsMessage(err.response?.data?.error || '保存に失敗しました。');
    } finally {
      setSavingSettings(false);
    }
  };

  const handleReviewJoinRequest = async (requestId: string, action: 'approve' | 'reject') => {
    try {
      await teamApi.reviewJoinRequest(token!, requestId, action);
      setJoinMessage(action === 'approve' ? '参加リクエストを承認しました。' : '参加リクエストを拒否しました。');
      await loadJoinRequests();
    } catch (err: any) {
      setJoinMessage(err.response?.data?.error || '処理に失敗しました。');
    }
  };

  const handleJoin = async () => {
    setJoinMessage('参加リクエストを送信しています...');
    try {
      await teamApi.join(token!);
      setJoinMessage('チームに参加しました。');
      await load();
    } catch (err: any) {
      setJoinMessage(err.response?.data?.error || '参加に失敗しました。');
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
    return (
      <Grid container spacing={3}>
        {items.map((item) => (
          <Grid item key={`${type}-${item.id}`} xs={12} sm={6} md={4} lg={3}>
            <Card
                component="div"
                onClick={() => navigate(link.detail(item.id))}
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
                <CardActions sx={{ mt: 'auto', alignItems: 'center' }}>
                  <Box sx={{ display: 'flex', alignItems: 'center', flexGrow: 1, minWidth: 0, mr: 1 }}>
                    <PersonIcon sx={{ fontSize: 16, mr: 0.5, color: 'text.secondary' }} />
                    <Typography variant="body2" noWrap sx={{ flexGrow: 1, minWidth: 0 }} title={usernames[item.uploader_id ?? ''] || item.uploader_id || undefined}>
                      {usernames[item.uploader_id ?? ''] || '投稿者不明'}
                    </Typography>
                  </Box>
                  {item.uploader_id === user?.userID && (
                    <Button size="small" onClick={(e) => { e.stopPropagation(); navigate(link.edit(item.id)); }} startIcon={<EditIcon />}>
                      編集
                    </Button>
                  )}
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
             <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 2 }}>
               <Box sx={{ minWidth: 0, flexGrow: 1 }}>
                 <Typography variant="h4" component="h1" fontWeight="bold" noWrap title={team.name}>{team.name}</Typography>
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
                <>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mr: 1 }}>
                    <Button variant="contained" size="small" onClick={openSettings} startIcon={<EditIcon />} color="primary">
                      チーム設定
                    </Button>
                    <Button variant="contained" size="small" onClick={handleDeleteTeam} startIcon={<DeleteIcon />} color="error">
                      チームを削除
                    </Button>
                  </Box>
                  {!canPost && team?.is_public && (
                    <Button size="small" variant="outlined" startIcon={<GroupIcon />} onClick={handleJoin}>
                      チームへ加入
                    </Button>
                  )}
                </>
              )}
              {!canManageTeam && !canPost && team?.is_public && (
                <Button size="small" variant="outlined" startIcon={<GroupIcon />} onClick={handleJoin}>
                  チームへ加入
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
                {canManageTeam && (
                <>
                  <Typography variant="h6" gutterBottom>参加リクエスト管理</Typography>
                  {joinRequestsLoading ? (
                    <CircularProgress size={20} />
                  ) : joinRequests.length === 0 ? (
                    <Alert severity="info">参加リクエストはありません。</Alert>
                  ) : (
                    joinRequests.map((r) => (
                      <Box key={r.id} sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', py: 1 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center' }}>
                          <Avatar sx={{ width: 32, height: 32, mr: 1 }}>{(r.user_id || '?').charAt(0).toUpperCase()}</Avatar>
                          <Typography variant="body2" noWrap title={usernames[r.user_id] || r.user_id}>{usernames[r.user_id] || r.user_id}</Typography>
                          <Chip size="small" label={r.status} sx={{ ml: 1 }} color={r.status === 'pending' ? 'warning' : 'default'} />
                        </Box>
                        <Box sx={{ display: 'flex', gap: 1 }}>
                          <Button size="small" color="success" onClick={() => handleReviewJoinRequest(r.id, 'approve')}>承認</Button>
                          <Button size="small" color="error" onClick={() => handleReviewJoinRequest(r.id, 'reject')}>拒否</Button>
                        </Box>
                      </Box>
                    ))
                  )}

                  {joinMessage && <Alert severity="info" sx={{ mt: 1 }}>{joinMessage}</Alert>}

                  <Divider sx={{ my: 2 }} />
                </>
              )}
              <Box sx={{ mb: 2 }}>
                <Typography variant="h6" gutterBottom>メンバー管理</Typography>
                {members.length === 0 ? (
                  <Alert severity="info">メンバーがいません。</Alert>
                ) : (
                  members.map((m) => {
                    const isSelf = m.user_id === user?.userID;
                    return (
                      <Box key={`${m.user_id}-${m.role}`} sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', py: 1, gap: 1 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', flexGrow: 1, minWidth: 0 }}>
                          <Avatar sx={{ width: 32, height: 32, mr: 1 }}>{(m.user_id || '?').charAt(0).toUpperCase()}</Avatar>
                          <Box>
                            <Typography variant="body1" fontWeight="bold" noWrap title={usernames[m.user_id] || m.user_id}>
                              {usernames[m.user_id] || m.user_id}
                            </Typography>
                            <Typography variant="caption" color="text.secondary" noWrap>
                              {m.user_id}
                            </Typography>
                          </Box>
                          <Chip size="small" label={ROLE_LABEL[m.role] || m.role} sx={{ ml: 1 }} />
                        </Box>
                        {canManageMembers && (
                          <TextField
                            select
                            size="small"
                            value={m.role}
                            disabled={isSelf}
                            onChange={(e) => handleRoleChange(m.user_id, e.target.value)}
                            sx={{ minWidth: 150 }}
                            label="権限"
                          >
                            <MenuItem value="owner">ホスト</MenuItem>
                            <MenuItem value="admin">オペレーター</MenuItem>
                            <MenuItem value="member">メンバー</MenuItem>
                          </TextField>
                        )}
                        {canManageTeam && m.role !== 'owner' && !isSelf && (
                          <Tooltip title="メンバーを削除">
                            <IconButton onClick={() => handleRemoveMember(m.user_id)} size="small">
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    );
                  })
                )}
              </Box>
            </>
          )}
        </>
      )}

      <Box sx={{ mt: 3 }}>
        <Button onClick={() => navigate('/teams')}>← チーム一覧に戻る</Button>
      </Box>

      {/* チーム設定ダイアログ */}
      <Dialog open={settingsOpen} onClose={() => setSettingsOpen(false)} maxWidth="xs" fullWidth PaperProps={{ sx: { maxHeight: 'calc(100vh - 100px)', overflow: 'auto' } }}>
        <DialogTitle>チーム設定</DialogTitle>
        <DialogContent sx={{ pt: '12px !important' }}>
          <TextField
            label="チーム名"
            fullWidth
            size="small"
            value={teamName}
            onChange={(e) => setTeamName(e.target.value)}
            sx={{ mb: 2, overflow: 'visible', '& .MuiInputBase-root': { overflow: 'visible' } }}
          />
          <TextField
            label="チーム説明"
            fullWidth
            size="small"
            multiline
            rows={3}
            value={teamDescription}
            onChange={(e) => setTeamDescription(e.target.value)}
          />
          <FormControlLabel
            control={
              <Switch
                checked={isPublic}
                onChange={(e) => setIsPublic(e.target.checked)}
              />
            }
            label="公開チームにする（誰でも閲覧可）"
          />
          <Box sx={{ mt: 2 }}>
            <FormControlLabel
              control={
                <Switch
                  checked={autoApprove}
                  onChange={(e) => setAutoApprove(e.target.checked)}
                  disabled={!isPublic}
                />
              }
              label="参加を自動承認する（公開チームのみ）"
            />
            {!isPublic && (
              <Typography variant="caption" color="text.secondary">
                非公開チームでは参加リクエストを手動で承認する必要があります。
              </Typography>
            )}
          </Box>
          <FormControlLabel
            control={
              <Switch
                checked={allowMemberInvite}
                onChange={(e) => setAllowMemberInvite(e.target.checked)}
              />
            }
            label="メンバーへの招待URL共有を許可する"
          />
          <Typography variant="caption" color="text.secondary">
            ONにすると、ホスト以外のメンバーも招待URLを共有できます。
          </Typography>
          {settingsMessage && <Alert severity="info" sx={{ mt: 2 }}>{settingsMessage}</Alert>}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setSettingsOpen(false)}>キャンセル</Button>
          <Button variant="contained" onClick={saveSettings} disabled={savingSettings}>保存</Button>
        </DialogActions>
      </Dialog>
    </Container>
  );
};

export default TeamDetailPage;