import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

/**
 * ゲームのタイトル・説明・サムネイルを編集するページ。
 * サムネイルは SFSP のセキュリティスキャンを経て保存されるため、送信後にスキャン完了を待つ。
 */
const EditGamePage = () => {
  const { id } = useParams();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  // 新しく選択されたサムネイル画像（未選択なら null）
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  // サーバーに保存済みのサムネイル URL（スキャン後に URL が得られない場合の代替に使う）
  const [existingThumbnailUrl, setExistingThumbnailUrl] = useState('');
  // 画面に表示中のサムネイル URL（選択直後はローカルの blob URL になる）
  const [thumbnailUrl, setThumbnailUrl] = useState('');
  const [loading, setLoading] = useState(true);
  const [updating, setUpdating] = useState(false);
  const [isScanningThumbnail, setIsScanningThumbnail] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const navigate = useNavigate();
  const { token } = useAuth();

  // 初回表示時に現在のゲーム情報を取得してフォームに反映する。
  useEffect(() => {
    const fetchGame = async () => {
      try {
        const response = await axios.get(`/api/games/${id}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        setTitle(response.data.title);
        setDescription(response.data.description);
        setExistingThumbnailUrl(response.data.thumbnail_url);
        setThumbnailUrl(response.data.thumbnail_url);
      } catch (err) {
        setError('ゲーム情報の取得に失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchGame();
  }, [id, token]);

  /** ファイル選択時に、選んだ画像を保持してローカルプレビューを表示する。 */
  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      setThumbnail(file);
      setThumbnailUrl(URL.createObjectURL(file));
    }
  };

  // thumbnail_sfsp_job_id が NULL になるまでポーリングする。
  // このフラグがクリアされるのはゲームワーカーがクリーンなサムネイルを
  // game-storage に書き込んだ後だけなので、これでストレージ書き込み完了を
  // 保証でき、フライングによる旧画像の表示を防止できる。
  const MAX_POLL_MS = 120000;
  /** 2 秒ごとにゲーム情報を取得し、サムネイルのスキャン完了（またはタイムアウト）で解決する。中断時は reject する。 */
  const pollGame = async (signal: AbortSignal): Promise<{ thumbnailUrl?: string }> => {
    return new Promise((resolve, reject) => {
      const startedAt = Date.now();
      const pollInterval = setInterval(async () => {
        try {
          const gameRes = await axios.get(`/api/games/${id}`, { signal });
          const game = gameRes.data;
          // ゲームワーカーがサムネイル処理を完了（job_id クリア）すれば解決。
          // この時点で thumbnail_url は新サムネイルに更新されている。
          if (!game.thumbnail_sfsp_job_id) {
            clearInterval(pollInterval);
            resolve({ thumbnailUrl: game.thumbnail_url || existingThumbnailUrl });
            return;
          }
          // 安全網: ゲームワーカーが応答しない無限スピンを防止する。
          if (Date.now() - startedAt > MAX_POLL_MS) {
            clearInterval(pollInterval);
            resolve({ thumbnailUrl: game.thumbnail_url || existingThumbnailUrl });
          }
        } catch (err) {
          if (!axios.isCancel(err)) {
            console.error('Polling game failed:', err);
          }
        }
      }, 2000);

      // 中断（AbortController.abort）されたらポーリングを止めて reject する
      signal.addEventListener('abort', () => {
        clearInterval(pollInterval);
        reject(new Error('Polling aborted'));
      });
    });
  };

  /** フォーム送信時の処理。サムネイルがあれば PUT /api/games/:id で送信し、スキャン完了を待ってから詳細ページへ遷移する。 */
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setUpdating(true);
    setError('');
    setSuccess('');

    const controller = new AbortController();

    // 1. 送信するフォームデータを組み立てる
    const formData = new FormData();
    formData.append('title', title);
    formData.append('description', description);
    if (thumbnail) {
      formData.append('thumbnail', thumbnail);
    }

    try {
      let newThumbnailUrl = thumbnailUrl;

      // 2. サムネイルが選択されている場合のみ PUT を送り、スキャン中ならスキャン完了まで待つ
      if (thumbnail) {
        setIsScanningThumbnail(true);
        const res = await axios.put(`/api/games/${id}`, formData, {
          headers: {
            'Content-Type': 'multipart/form-data',
            Authorization: `Bearer ${token}`,
          },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          await pollGame(controller.signal);
        } else if (res.data.thumbnail_url) {
          newThumbnailUrl = res.data.thumbnail_url;
        }
      }

      // 3. 書き込まれたばかりのサムネイルを先読みする（旧画像のブラウザキャッシュを
      // 避けるためにキャッシュバスターを付ける）。読み込みが終わるまでオーバーレイを
      // 表示したままにし、完全に準備できてから表示画像を差し替えることで、
      // 切り替え中に旧サムネイルが一瞬表示されないようにする。
      const bust = newThumbnailUrl.includes('?') ? '&' : '?';
      const freshUrl = newThumbnailUrl + bust + 't=' + Date.now();
      const preload = new Image();
      // 先読み完了（成功・失敗とも）で表示を切り替え、2 秒後に詳細ページへ遷移する
      const reveal = () => {
        setIsScanningThumbnail(false);
        setThumbnailUrl(freshUrl);
        setSuccess('ゲーム情報が更新されました。');
        setTimeout(() => navigate(`/games/${id}`), 2000);
      };
      preload.onload = reveal;
      preload.onerror = reveal;
      preload.src = freshUrl;
    } catch (err) {
      if (!axios.isCancel(err)) {
        setError('更新に失敗しました。');
      }
      setIsScanningThumbnail(false);
    } finally {
      setUpdating(false);
    }
  };

  /** 確認ダイアログの後、DELETE /api/games/:id/thumbnail でサムネイルを削除する。 */
  const handleDeleteThumbnail = async () => {
    if (!window.confirm('本当にサムネイルを削除しますか？')) {
      return;
    }
    setUpdating(true);
    try {
      await axios.delete(`/api/games/${id}/thumbnail`, {
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
        ゲーム情報を編集
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
          {/* サムネイル選択ボタン。スキャン中はボタンの上に半透明のオーバーレイを重ねる */}
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
        {/* 表示中のサムネイルと削除ボタン */}
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

export default EditGamePage;
