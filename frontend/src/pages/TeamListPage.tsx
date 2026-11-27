import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Container, Typography, Box, Paper, Card, CardActionArea, Grid, Button, TextField,
  Dialog, DialogTitle, DialogContent, DialogActions, FormControlLabel, Checkbox,
  Alert, Chip, CircularProgress, useTheme,
} from '@mui/material';
import GroupIcon from '@mui/icons-material/Group';
import { teamApi, Team } from '../api/team';
import { useAuth } from '../context/AuthContext';

/**
 * チーム一覧ページ。
 * 公開チームと自分が所属するチーム（非公開含む）をまとめてカード表示し、
 * ダイアログから新しいチームを作成できる。
 */
const TeamListPage = () => {
  const { isAuthenticated } = useAuth();
  const navigate = useNavigate();
  const theme = useTheme();

  // 一覧表示の状態
  const [teams, setTeams] = useState<Team[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // チーム作成ダイアログの状態と入力値
  const [createOpen, setCreateOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [isPublic, setIsPublic] = useState(true);
  const [autoApprove, setAutoApprove] = useState(false);

  /**
   * 公開チームと所属チームを並行取得し、ID で重複を除いて作成日の新しい順に並べる。
   * 片方の取得に失敗しても（未ログインで /mine が失敗する場合など）空配列として扱い、もう片方は表示する。
   */
  const loadTeams = async () => {
    setLoading(true);
    setError('');
    try {
      // 1. 公開チームと所属チームを並行して取得する
      const [publicRes, mineRes] = await Promise.all([
        teamApi.listPublic().catch(() => ({ data: [] })),
        teamApi.listMine().catch(() => ({ data: [] })),
      ]);
      // 2. ID で重複を除く。同じチームが両方に含まれる場合は is_public が true の方を優先する
      const sources = [...(publicRes.data || []), ...(mineRes.data || [])];
      const byId = new Map();
      sources.forEach((t) => {
        const existing = byId.get(t.id);
        if (!existing || (t.is_public && !existing.is_public)) {
          byId.set(t.id, t);
        }
      });
      // 3. 作成日の新しい順に並べて表示する
      setTeams(Array.from(byId.values()).sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime()));
    } catch (err) {
      console.error(err);
      setError('チームの一覧を取得できませんでした。');
    } finally {
      setLoading(false);
    }
  };

  // 初回表示時に一覧を読み込む
  useEffect(() => {
    loadTeams();
  }, []);

  /**
   * 作成ダイアログの「作成」ボタンの処理。
   * チーム名を検証してから作成 API を呼び、成功したら入力値を初期値に戻して一覧を再読み込みする。
   */
  const handleCreate = async () => {
    if (!name.trim()) {
      setError('チーム名を入力してください。');
      return;
    }
    setCreating(true);
    setError('');
    try {
      await teamApi.create({ name: name.trim(), description: description.trim(), is_public: isPublic, auto_approve: autoApprove });
      // 作成に成功したらダイアログを閉じ、次回のために入力値を初期値へ戻す
      setCreateOpen(false);
      setName('');
      setDescription('');
      setIsPublic(true);
      setAutoApprove(false);
      await loadTeams();
    } catch (err: any) {
      setError(err.response?.data?.error || 'チーム作成に失敗しました。');
    } finally {
      setCreating(false);
    }
  };

  // 作成ダイアログを開く。未ログインならログインページへ遷移させる
  const handleOpenCreate = () => {
    if (!isAuthenticated) {
      navigate('/login');
      return;
    }
    setCreateOpen(true);
  };

  return (
    <Container maxWidth="md" sx={{ py: 6 }}>
      {/* 見出しと作成ボタン */}
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h4" component="h1" fontWeight="bold">
          <GroupIcon sx={{ mr: 1, verticalAlign: 'middle' }} />
          チーム
        </Typography>
        <Button variant="contained" onClick={handleOpenCreate}>
          チームを作成
        </Button>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>
      )}

      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        あなたが作成または参加したチーム（非公開含む）と、公開チームを表示します。
      </Typography>

      {/* 読み込み中・0件・一覧の表示切り替え */}
      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}>
          <CircularProgress />
        </Box>
      ) : teams.length === 0 ? (
        <Paper sx={{ p: 6, textAlign: 'center' }}>
          <Typography color="text.secondary">まだチームがありません。</Typography>
          <Button onClick={handleOpenCreate} sx={{ mt: 2 }}>最初のチームを作成</Button>
        </Paper>
      ) : (
        <Grid container spacing={3}>
          {teams.map((team) => (
            <Grid item xs={12} sm={6} md={4} key={team.id}>
              {/* カード全体をクリックするとチーム詳細（URL は ID ではなくトークン）へ遷移する */}
              <Card sx={{ height: '100%' }}>
                <CardActionArea onClick={() => navigate(`/teams/${team.token}`)} sx={{ height: '100%' }}>
                  <Box sx={{ p: 3 }}>
                    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 1 }}>
                      <Typography variant="h6" component="h2" noWrap>{team.name}</Typography>
                      <Chip
                        size="small"
                        label={team.is_public ? '公開' : '非公開'}
                        sx={{
                          bgcolor: team.is_public ? theme.palette.primary.main : theme.palette.grey[400],
                          color: 'white',
                        }}
                      />
                    </Box>
                    {team.description && (
                      <Typography variant="body2" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                        {team.description}
                      </Typography>
                    )}
                    <Typography variant="caption" color="text.secondary" sx={{ mt: 2, display: 'block' }}>
                      投稿日: {new Date(team.created_at).toLocaleDateString('ja-JP')}
                    </Typography>
                  </Box>
                </CardActionArea>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}

      {/* チーム作成ダイアログ */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>チームを作成</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus
            margin="normal"
            fullWidth
            label="チーム名"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <TextField
            margin="normal"
            fullWidth
            multiline
            minRows={2}
            label="説明"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
          <FormControlLabel
            control={
              <Checkbox checked={isPublic} onChange={(e) => setIsPublic(e.target.checked)} color="primary" />
            }
            label="公開チームにする（トークンを持つ誰でも閲覧可能）"
          />
          {/* 自動承認は公開チームでのみ有効なため、非公開のときは無効化する */}
          <FormControlLabel
            control={
              <Checkbox
                checked={autoApprove}
                onChange={(e) => setAutoApprove(e.target.checked)}
                color="primary"
                disabled={!isPublic}
              />
            }
            label="自動参加を許可する（公開チームのみ有効）"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>キャンセル</Button>
          <Button onClick={handleCreate} variant="contained" disabled={creating}>
            {creating ? <CircularProgress size={20} /> : '作成'}
          </Button>
        </DialogActions>
      </Dialog>
    </Container>
  );
};

export default TeamListPage;