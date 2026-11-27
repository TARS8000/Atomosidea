import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import axios from 'axios';
import { Container, TextField, Button, Typography, Box, CircularProgress, Alert } from '@mui/material';
import { useAuth } from '../context/AuthContext';

/** サムネイルのスキャン完了を待つ最大時間（ミリ秒）。超えたらタイムアウトとして reject する。 */
const MAX_POLL_MS = 120000;

/**
 * 静的サイトのタイトル・説明・サムネイルを編集するページ。
 * サムネイルがある場合は先にサムネイルだけを送ってスキャン結果を待ち、
 * 受理された場合のみタイトル・説明を更新する（2 段階更新）。
 */
const EditStaticSitePage = () => {
  const { id } = useParams();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  // 新しく選択されたサムネイル画像（未選択なら null）
  const [thumbnail, setThumbnail] = useState<File | null>(null);
  // 編集開始時点でサーバーに保存されていたサムネイル URL（スキャン受理判定の比較元）
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

  // 初回表示時に現在のサイト情報を取得してフォームに反映する。
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

  /** ファイル選択時に、選んだ画像を保持してローカルプレビューを表示する。 */
  const handleThumbnailChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      setThumbnail(file);
      setThumbnailUrl(URL.createObjectURL(file));
    }
  };

  /**
   * 2 秒ごとにサイト情報を取得し、thumbnail_sfsp_job_id が空になったら
   * サムネイルの URL と受理されたかどうかを返す。タイムアウト・中断時は reject する。
   */
  const pollStaticSite = async (signal: AbortSignal): Promise<{ thumbnailUrl: string; accepted: boolean }> => {
    return new Promise((resolve, reject) => {
      const startTime = Date.now();
      const pollInterval = setInterval(async () => {
        try {
          const siteRes = await axios.get(`/api/static-sites/${id}`, { signal });
          const site = siteRes.data;
          if (!site.thumbnail_sfsp_job_id) {
            clearInterval(pollInterval);
            // job_id はスキャン終了時にクリアされる。新しいサムネイルが受理されたと
            // みなすのは、保存された URL が開始時点の URL から変わった場合だけ
            // （ワーカーは成功時に新しいオブジェクトを書き込み、スキャンでファイルが
            // 拒否された場合は古い URL をそのまま残す）。
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
        // 最大待機時間を超えたらポーリングを止めて reject する（解決済みなら reject は無視される）
        if (Date.now() - startTime > MAX_POLL_MS) {
          clearInterval(pollInterval);
          reject(new Error('サムネイルアップロードの待機がタイムアウトしました。'));
        }
      }, 2000);

      // 中断（AbortController.abort）されたらポーリングを止めて reject する
      signal.addEventListener('abort', () => {
        clearInterval(pollInterval);
        reject(new Error('Polling aborted'));
      });
    });
  };

  /** フォーム送信時の処理。サムネイルの有無で処理を分け、更新後に詳細ページへ遷移する。 */
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setUpdating(true);
    setError('');
    setSuccess('');

    const controller = new AbortController();

    try {
      if (thumbnail) {
        // ステップ 1: サムネイルだけをアップロードする（SFSP スキャンが始まる）。
        // サムネイルが拒否された場合にサイトが中途半端に更新されないよう、
        // タイトル・説明の更新はスキャン完了まで後回しにする。
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
          // SFSP スキャンの完了を待つ。ワーカーはクリーンなサムネイルをストレージに
          // 書き込んでジョブ ID をクリアするので、その時点で保存された URL は
          // 開始時点の URL と異なるものになる。
          const result = await pollStaticSite(controller.signal);
          if (!result.accepted) {
            // 拒否された: 以前のサムネイルを維持してエラーを表示する。
            setIsScanningThumbnail(false);
            setThumbnailUrl(existingThumbnailUrl);
            setError('サムネイルがセキュリティスキャンで拒否されました。別の画像を選択してください。');
            return;
          }
          // 受理された: ストレージに書き込まれたばかりのサムネイルを表示する。
          setThumbnailUrl(result.thumbnailUrl);
        } else if (res.data.thumbnail_url) {
          // SFSP が即座に結果を返した場合（例: 重複排除されたクリーンなファイル）。
          setThumbnailUrl(res.data.thumbnail_url);
        }

        // ステップ 2: スキャンが完了した（またはスキップされた）。タイトル・説明を確定する。
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
        // サムネイルのアップロードなし: タイトル・説明だけを更新する。
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

      // UI が落ち着いたら詳細ページへ遷移する（成功メッセージが見えるよう少し待つ）。
      // プレビューは既にサーバー側のサムネイルを指しているので、
      // クライアント側での先読みは不要。
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

  /** 確認ダイアログの後、DELETE /api/static-sites/:id/thumbnail でサムネイルを削除する。 */
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

export default EditStaticSitePage;
