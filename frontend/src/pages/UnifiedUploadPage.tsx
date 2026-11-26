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

// 公開範囲。'public' は全体公開、'team' は選択したチーム限定の投稿。
type Scope = 'public' | 'team';

/** コンテンツ種別ごとのアップロード設定。 */
interface UploadField {
  /** 画面に表示する種別名 */
  label: string;
  /** multipart フォームでファイル本体を入れるフィールド名（バックエンドごとに異なる） */
  fileField: string;
  /** アップロード先の API エンドポイント */
  endpoint: string;
  /** ファイル選択ダイアログの accept 属性 */
  accept: string;
  /** ファイル選択ボタンの下に出す説明文 */
  hint: string;
  /** アップロード成功後に遷移する画面のパスを ID から組み立てる */
  redirect: (id: string) => string;
}

// 種別キー（video / game / site）ごとのアップロード設定。?type= クエリの値もこのキーで照合する。
const UPLOAD_TYPES: Record<string, UploadField> = {
  video: {
    label: '動画',
    fileField: 'video',
    endpoint: '/api/videos/upload',
    accept: 'video/mp4,video/webm',
    hint: '動画ファイルを選択してください。',
    redirect: (id) => `/videos/${id}`,
  },
  // ゲームはアップロード後に表示位置・サイズの調整画面へ遷移する
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

/**
 * 動画・ゲーム・静的サイトを1画面でアップロードする統合ページ。
 * クエリ ?type= で初期の種別を、?team= でチーム限定投稿の対象チーム（トークン）を指定できる。
 */
const UnifiedUploadPage = () => {
  const navigate = useNavigate();
  const theme = useTheme();
  const [searchParams] = useSearchParams();

  // 入力フォームとアップロード進捗の状態
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

  // チーム限定投稿用の状態。selectedTeam にはチーム ID ではなくトークンが入る
  const [teams, setTeams] = useState<Team[]>([]);
  const [selectedTeam, setSelectedTeam] = useState('');
  const [loadingTeams, setLoadingTeams] = useState(false);
  const [joinStatus, setJoinStatus] = useState<{ message: string; error?: string } | null>(null);

  // ?team= で渡されたチームトークン（チーム詳細ページなどからの遷移で使う）
  const teamToken = searchParams.get('team');

  // ?type= が既知の種別なら初期選択に反映する
  useEffect(() => {
    const typeParam = searchParams.get('type');
    if (typeParam && UPLOAD_TYPES[typeParam]) {
      setType(typeParam);
    }
  }, [searchParams]);

  // ?team= があればチーム限定に切り替え、そのチームを選択済みにする
  useEffect(() => {
    if (teamToken) {
      setScope('team');
      setSelectedTeam(teamToken);
    }
  }, [teamToken]);

  // チーム限定を選んだらチーム一覧を読み込み、公開に戻したらチーム選択を解除する
  useEffect(() => {
    if (scope === 'team') {
      loadTeams();
    } else {
      setSelectedTeam('');
    }
  }, [scope]);

  /**
   * 選択肢に出すチーム一覧を取得する。
   * 公開チーム一覧を基準にし、所属チームに含まれるものには _joined フラグを立てる。
   * 所属チームのうち公開チーム一覧に無いもの（非公開チーム）は選択肢に含まれない。
   */
  const loadTeams = async () => {
    setLoadingTeams(true);
    setError('');
    try {
      // 片方の取得に失敗しても空配列として扱い、画面全体はエラーにしない
      const [publicRes, mineRes] = await Promise.all([
        teamApi.listPublic().catch(() => ({ data: [] })),
        teamApi.listMine().catch(() => ({ data: [] })),
      ]);
      // 所属チームのトークン集合を作り、公開チームごとに参加済みかどうかを付ける
      const mineTokens = new Set((mineRes.data || []).map((t: Team) => t.token));
      const merged = publicRes.data.map((t: Team) => ({ ...t, _joined: mineTokens.has(t.token) }));
      setTeams(merged);
    } catch (err) {
      setError('チームの一覧を取得できませんでした。');
    } finally {
      setLoadingTeams(false);
    }
  };

  /**
   * 選択中のチームへ参加リクエストを送り、結果を joinStatus に表示する。
   * 成功時は一覧を再読み込みして「参加済み」表示を更新する。
   * 注: API が承認待ち（pending）を返した場合も「参加しました」と表示される。
   */
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

  // アップロードするファイル本体の選択を受け取る（先頭の1件だけを使う）
  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files) {
      setFile(e.target.files[0]);
    }
  };

  /**
   * サムネイル画像の選択を受け取る。
   * 100MB 以上なら拒否し、それ以外はプレビュー用の Object URL を作って表示する。
   */
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

  // プレビュー URL が差し替わるとき・画面を離れるときに古い Object URL を解放する（メモリリーク防止）
  useEffect(() => {
    return () => {
      if (thumbnailPreview) {
        URL.revokeObjectURL(thumbnailPreview);
      }
    };
  }, [thumbnailPreview]);

  /**
   * フォーム送信時の処理。選択中の種別のエンドポイントへ multipart でアップロードし、
   * 返ってきた ID をもとに種別ごとの画面へ遷移する。
   */
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    // 1. 入力を検証する
    if (!file) {
      setError('アップロードするファイルを選択してください。');
      return;
    }
    if (!title.trim()) {
      setError('タイトルを入力してください。');
      return;
    }

    // 2. 送信するフォームデータを組み立てる
    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    // チーム限定のときだけ team_id にチームトークンを入れる（空ならバックエンドは全体公開として扱う）
    if (scope === 'team') {
      formData.append('team_id', selectedTeam);
    }
    formData.append(UPLOAD_TYPES[type].fileField, file);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    // 3. 送信状態を初期化する
    setUploading(true);
    setError('');
    setSuccess('');
    setUploadProgress(0);

    // 4. アップロードし、進捗をプログレスバーへ反映する
    try {
      const response = await axios.post(UPLOAD_TYPES[type].endpoint, formData, {
        headers: { Authorization: `Bearer ${localStorage.getItem('token')}` },
        onUploadProgress: (progressEvent) => {
          // total が取れない場合に 0 除算しないよう 1 で代用する
          const percentCompleted = Math.round((progressEvent.loaded * 100) / (progressEvent.total ?? 1));
          setUploadProgress(percentCompleted);
        },
      });
      setSuccess('アップロードが開始されました。処理が完了するまでしばらくお待ちください。');
      // 5. 応答の ID のキー名はサービスごとに異なる（動画: videoID / ゲーム: gameId / サイト: siteId）
      const id = response.data.videoID || response.data.gameId || response.data.siteId;
      if (id) {
        navigate(UPLOAD_TYPES[type].redirect(id));
      }
    } catch (err) {
      // サーバーが返したエラーメッセージがあればそれを表示する
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || '不明なエラーが発生しました。');
      } else {
        setError('不明なエラーが発生しました。');
      }
    } finally {
      setUploading(false);
    }
  };

  // 現在選択中の種別の設定（ボタン文言・accept・説明文に使う）
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

        {/* スコープ（公開範囲） */}
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

        {/* チーム限定のときだけ、チーム選択と参加ボタンを表示する */}
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

                {/* 選択中チームの公開/非公開と自動参加可否を表示する */}
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

          {/* ファイル本体の選択（input は非表示にし、ボタン全体をラベルとして使う） */}
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

          {/* サムネイル画像の選択とプレビュー */}
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

          {/* アップロード中の進捗表示 */}
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