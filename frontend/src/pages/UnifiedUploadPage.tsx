import { useState, useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  Container,
  Typography,
  Box,
  Paper,
  TextField,
  Button,
  Alert,
  CircularProgress,
  LinearProgress,
  MenuItem,
  Radio,
  RadioGroup,
  FormControlLabel,
  FormControl,
  FormLabel,
  Divider,
  Chip,
  useTheme,
} from '@mui/material';
import UploadIcon from '@mui/icons-material/Upload';
import PublicIcon from '@mui/icons-material/Public';
import LockIcon from '@mui/icons-material/Lock';
import GroupIcon from '@mui/icons-material/Group';
import axios from 'axios';
import { teamApi, Team } from '../api/team';

type Scope = 'public' | 'team';

interface UploadField {
  label: string;
  fileField: string;
  endpoint: string;
  accept: string;
  hint: string;
  redirect: (id: string) => string;
}

const UPLOAD_TYPES: Record<string, UploadField> = {
  video: {
    label: '動画',
    fileField: 'video',
    endpoint: '/api/videos/upload',
    accept: 'video/mp4,video/webm',
    hint: '動画ファイルを選択してください。',
    redirect: (id) => `/videos/${id}`,
  },
  game: {
    label: 'ゲーム',
    fileField: 'game',
    endpoint: '/api/games/upload',
    accept: '.zip,application/x-zip-compressed',
    hint: 'ZIPファイル（Unity WebGL版）を選択してください。',
    redirect: (id) => `/adjust-game/${id}`,
  },
  site: {
    label: '静的サイト',
    fileField: 'file',
    endpoint: '/api/static-sites/upload',
    accept: '.zip,application/x-zip-compressed',
    hint: 'HTML/CSS/JSファイルを含むZIPファイルを選択してください。',
    redirect: (id) => `/static-sites/${id}`,
  },
};

const UnifiedUploadPage = () => {
  const navigate = useNavigate();
  const theme = useTheme();
  const [searchParams] = useSearchParams();

  const [scope, setScope] = useState<Scope>('public');
  const [type, setType] = useState<string>('video');
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  const [thumbnailPreview, setThumbnailPreview] = useState<string | null>(null);
  const [uploadProgress, setUploadProgress] = useState(0);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const [teams, setTeams] = useState<Team[]>([]);
  const [selectedTeam, setSelectedTeam] = useState('');
  const [loadingTeams, setLoadingTeams] = useState(false);
  const [joinStatus, setJoinStatus] = useState<{ message: string; error?: string } | null>(null);

  const teamToken = searchParams.get('team');

  useEffect(() => {
    const typeParam = searchParams.get('type');
    if (typeParam && UPLOAD_TYPES[typeParam]) {
      setType(typeParam);
    }
  }, [searchParams]);

  useEffect(() => {
    if (teamToken) {
      setScope('team');
      setSelectedTeam(teamToken);
    }
  }, [teamToken]);

  useEffect(() => {
    if (scope === 'team') {
      loadTeams();
    } else {
      setSelectedTeam('');
    }
  }, [scope]);

  const loadTeams = async () => {
    setLoadingTeams(true);
    setError('');
    try {
      const [publicRes, mineRes] = await Promise.all([
        teamApi.listPublic().catch(() => ({ data: [] })),
        teamApi.listMine().catch(() => ({ data: [] })),
      ]);
      const mineTokens = new Set((mineRes.data || []).map((t: Team) => t.token));
      const merged = publicRes.data.map((t: Team) => ({ ...t, _joined: mineTokens.has(t.token) }));
      setTeams(merged);
    } catch (err) {
      setError('チームの一覧を取得できませんでした。');
    } finally {
      setLoadingTeams(false);
    }
  };

  const handleJoin = async () => {
    if (!selectedTeam) return;
    setJoinStatus({ message: '参加リクエストを送信しています...' });
    try {
      await teamApi.join(selectedTeam);
      setJoinStatus({ message: 'チームに参加しました。' });
      await loadTeams();
    } catch (err: any) {
      const msg = err.response?.data?.error || 'チームへの参加に失敗しました。';
      setJoinStatus({ message: msg, error: msg });
    }
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      setFile(e.target.files[0]);
    }
  };

  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      const imageFile = e.target.files[0];
      if (imageFile.size > 100 * 1024 * 1024) {
        setError('サムネイル画像のサイズは100MB未満である必要があります。');
        return;
      }
      setThumbnail(imageFile);
      setThumbnailPreview(URL.createObjectURL(imageFile));
    }
  };

  useEffect(() => {
    return () => {
      if (thumbnailPreview) {
        URL.revokeObjectURL(thumbnailPreview);
      }
    };
  }, [thumbnailPreview]);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file) {
      setError('アップロードするファイルを選択してください。');
      return;
    }
    if (!title.trim()) {
      setError('タイトルを入力してください。');
      return;
    }

    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    if (scope === 'team') {
      formData.append('team_id', selectedTeam);
    }
    formData.append(UPLOAD_TYPES[type].fileField, file);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    setUploading(true);
    setError('');
    setSuccess('');
    setUploadProgress(0);

    try {
      const response = await axios.post(UPLOAD_TYPES[type].endpoint, formData, {
        headers: { Authorization: `Bearer ${localStorage.getItem('token')}` },
        onUploadProgress: (progressEvent) => {
          const percentCompleted = Math.round((progressEvent.loaded * 100) / (progressEvent.total ?? 1));
          setUploadProgress(percentCompleted);
        },
      });
      setSuccess('アップロードが開始されました。処理が完了するまでしばらくお待ちください。');
      const id = response.data.videoID || response.data.gameId || response.data.siteId;
      if (id) {
        navigate(UPLOAD_TYPES[type].redirect(id));
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

  const currentType = UPLOAD_TYPES[type];

  return (
    <Container maxWidth="sm" sx={{ py: 6 }}>
      <Typography variant="h4" component="h1" gutterBottom sx={{ display: 'flex', alignItems: 'center' }}>
        <UploadIcon sx={{ mr: 1 }} />
        アップロード
      </Typography>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
      {success && <Alert severity="success" sx={{ mb: 2 }}>{success}</Alert>}

      <Paper sx={{ p: 3 }}>
        {/* コンテンツ種別 */}
        <FormControl component="fieldset" sx={{ mb: 3 }}>
          <FormLabel component="legend">コンテンツ種別</FormLabel>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, mt: 1 }}>
            {Object.entries(UPLOAD_TYPES).map(([key, t]) => (
              <Button
                key={key}
                variant={type === key ? 'contained' : 'outlined'}
                onClick={() => setType(key)}
                sx={{ flexShrink: 0 }}
              >
                {t.label}
              </Button>
            ))}
          </Box>
        </FormControl>

        <Divider sx={{ my: 3 }} />

        {/* スコープ */}
        <FormControl component="fieldset">
          <FormLabel component="legend">公開範囲</FormLabel>
          <RadioGroup
            value={scope}
            onChange={(e) => setScope(e.target.value as Scope)}
            sx={{ mt: 1 }}
          >
            <FormControlLabel value="public" control={<Radio />} label={
              <Box sx={{ display: 'flex', alignItems: 'center' }}>
                <PublicIcon sx={{ mr: 1, fontSize: 18 }} />
                公開（すべてのユーザーが閲覧可能）
              </Box>
            } />
            <FormControlLabel value="team" control={<Radio />} label={
              <Box sx={{ display: 'flex', alignItems: 'center' }}>
                <LockIcon sx={{ mr: 1, fontSize: 18 }} />
                チーム限定
              </Box>
            } />
          </RadioGroup>
        </FormControl>

        {scope === 'team' && (
          <Box sx={{ mt: 3, pl: 2, borderLeft: `3px solid ${theme.palette.divider}` }}>
            {loadingTeams ? (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 2 }}>
                <CircularProgress size={20} />
                <Typography variant="body2" color="text.secondary">チームを読み込み中...</Typography>
              </Box>
            ) : (
              <>
                <TextField
                  select
                  fullWidth
                  size="small"
                  label="チームを選択"
                  value={selectedTeam}
                  onChange={(e) => setSelectedTeam(e.target.value)}
                  sx={{ mb: 1 }}
                >
                  {teams.map((t) => (
                    <MenuItem key={t.token} value={t.token}>
                      {t.name}
                      {t._joined ? ' (参加済み)' : ''}
                    </MenuItem>
                  ))}
                </TextField>

                {selectedTeam && (
                  <Box sx={{ mt: 1 }}>
                    {teams.map((t) => (
                      t.token === selectedTeam && (
                        <Box key={t.token} sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                          <Chip
                            size="small"
                            icon={t.is_public ? <PublicIcon /> : <LockIcon />}
                            label={t.is_public ? '公開チーム' : '非公開チーム'}
                          />
                          {t.auto_approve && (
                            <Chip size="small" label="自動参加可" color="success" />
                          )}
                        </Box>
                      )
                    ))}
                  </Box>
                )}

                {joinStatus && (
                  <Alert severity={joinStatus.error ? 'warning' : 'info'} sx={{ mb: 1 }}>
                    {joinStatus.message}
                  </Alert>
                )}

                {selectedTeam && (
                  <Button
                    variant="outlined"
                    startIcon={<GroupIcon />}
                    onClick={handleJoin}
                    disabled={uploading}
                  >
                    チームへ加入
                  </Button>
                )}
              </>
            )}
          </Box>
        )}

        <Divider sx={{ my: 3 }} />

        {/* フォーム */}
        <form onSubmit={handleSubmit}>
          <TextField
            label="タイトル"
            variant="outlined"
            fullWidth
            required
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            disabled={uploading}
          />
          <TextField
            label="説明"
            variant="outlined"
            fullWidth
            multiline
            rows={4}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            disabled={uploading}
            sx={{ mt: 2 }}
          />

          <Box sx={{ mt: 2 }}>
            <Button variant="contained" component="label">
              {currentType.label}ファイルを選択
              <input type="file" hidden accept={currentType.accept} onChange={handleFileChange} />
            </Button>
            {file && <Typography sx={{ ml: 2, display: 'inline' }}>{file.name}</Typography>}
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
              {currentType.hint}
            </Typography>
          </Box>

          <Box sx={{ mt: 2 }}>
            <Button variant="outlined" component="label">
              サムネイル画像を選択
              <input type="file" hidden accept="image/*" onChange={handleThumbnailChange} />
            </Button>
            {thumbnail && <Typography sx={{ ml: 2, display: 'inline' }}>{thumbnail.name}</Typography>}
{thumbnailPreview && (
               <Box sx={{ mt: 1, display: 'flex', alignItems: 'center' }}>
<img
                   src={thumbnailPreview}
                   alt="サムネプレビュー"
                   style={{
                     maxWidth: 240,
                     maxHeight: 135,
                     objectFit: 'contain',
                     border: '1px solid',
                     borderColor: 'divider',
                     borderRadius: 8,
                     padding: 4,
                   }}
                 />
              </Box>
            )}
          </Box>

          {uploading && (
            <Box sx={{ width: '100%', my: 2 }}>
              <LinearProgress variant="determinate" value={uploadProgress} />
              <Typography variant="body2" color="text.secondary" align="center">{`${uploadProgress}%`}</Typography>
            </Box>
          )}

          <Button
            type="submit"
            variant="contained"
            color="primary"
            disabled={uploading || !file || !title.trim()}
            startIcon={uploading ? <CircularProgress size={20} /> : <UploadIcon />}
            sx={{ mt: 2 }}
          >
            {uploading ? 'アップロード中' : 'アップロード'}
          </Button>
        </form>
      </Paper>

      <Box sx={{ mt: 3 }}>
        <Button onClick={() => navigate(-1)}>← 戻る</Button>
      </Box>
    </Container>
  );
};

export default UnifiedUploadPage;