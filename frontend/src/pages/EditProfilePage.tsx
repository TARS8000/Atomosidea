import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import { Container, TextField, Button, Typography, Box, Alert, CircularProgress, Avatar } from '@mui/material';
import ImageCropperModal from '../components/ImageCropperModal';

/**
 * ログイン中ユーザーのプロフィール（ユーザー名・自己紹介・アイコン・背景画像）を編集するページ。
 * 画像は選択後にトリミングモーダルで切り抜き、送信時に SFSP のセキュリティスキャンを経て保存される。
 */
const EditProfilePage = () => {
  const { token, user, updateUser } = useAuth();
  const navigate = useNavigate();

  // テキスト項目
  const [username, setUsername] = useState('');
  const [bio, setBio] = useState('');

  // 画像項目: 送信用のファイル（トリミング後）と、表示用のプレビュー URL
  const [icon, setIcon] = useState<File | null>(null);
  const [iconPreview, setIconPreview] = useState('');
  const [background, setBackground] = useState<File | null>(null);
  const [backgroundPreview, setBackgroundPreview] = useState('');

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  // トリミングモーダルの状態（対象画像の data URL、縦横比、どちらの画像を編集中か）
  const [cropperOpen, setCropperOpen] = useState(false);
  const [imageToCrop, setImageToCrop] = useState('');
  const [cropAspect, setCropAspect] = useState(1);
  const [editingImageType, setEditingImageType] = useState<'icon' | 'background' | null>(null);

  // 各画像がセキュリティスキャン中かどうか
  const [isScanningIcon, setIsScanningIcon] = useState(false);
  const [isScanningBackground, setIsScanningBackground] = useState(false);

  // ログインユーザーが確定したら現在のプロフィールを取得してフォームに反映する。
  useEffect(() => {
    const fetchProfile = async () => {
      if (!user) return;
      try {
        const res = await axios.get(`/api/profile/${user.userID}`);
        setUsername(res.data.username || '');
        setBio(res.data.bio || '');
        setIconPreview(res.data.icon_url || '');
        setBackgroundPreview(res.data.background_image_url || '');
      } catch (err) {
        setError('プロフィールの読み込みに失敗しました。');
      } finally {
        setLoading(false);
      }
    };
    fetchProfile();
  }, [user]);

  /** 画像ファイルを選択したら data URL として読み込み、種類に応じた縦横比でトリミングモーダルを開く。 */
  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>, type: 'icon' | 'background') => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      const reader = new FileReader();
      reader.onload = () => {
        setImageToCrop(reader.result as string);
        setEditingImageType(type);
        setCropAspect(type === 'icon' ? 1 : 679 / 160); // アイコンは 1:1、背景は 679:160 の縦横比にする
        setCropperOpen(true);
      };
      reader.readAsDataURL(file);
    }
    // 同じファイルを再選択しても onChange が発火するよう値をクリアする
    e.target.value = '';
  };

  /** トリミング結果の Blob を JPEG ファイルにして、編集中の画像（アイコンまたは背景）に設定する。 */
  const handleCropSave = (croppedImageBlob: Blob) => {
    const file = new File([croppedImageBlob], `${editingImageType}.jpg`, { type: 'image/jpeg' });
    const previewUrl = URL.createObjectURL(file);

    if (editingImageType === 'icon') {
      setIcon(file);
      setIconPreview(previewUrl);
    } else if (editingImageType === 'background') {
      setBackground(file);
      setBackgroundPreview(previewUrl);
    }
    setCropperOpen(false);
  };

  /**
   * 2 秒ごとにプロフィールを取得し、icon_sfsp_job_id と background_sfsp_job_id が
   * 両方とも空になった（スキャン処理が終わった）時点で解決する。中断時は reject する。
   */
  const pollProfile = async (signal: AbortSignal): Promise<{ iconUrl?: string; backgroundImageUrl?: string }> => {
    return new Promise((resolve, reject) => {
      const pollInterval = setInterval(async () => {
        try {
          const profileRes = await axios.get(`/api/profile/${user?.userID}`, { signal });
          const profile = profileRes.data;
          if (!profile.icon_sfsp_job_id && !profile.background_sfsp_job_id) {
            clearInterval(pollInterval);
            resolve({
              iconUrl: profile.icon_url || iconPreview,
              backgroundImageUrl: profile.background_image_url || backgroundPreview,
            });
          }
        } catch (err) {
          if (!axios.isCancel(err)) {
            console.error('Polling profile failed:', err);
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

  /** フォーム送信時の処理。テキスト項目を更新した後、選択された画像を順にアップロードしてスキャン完了を待つ。 */
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess('');

    const controller = new AbortController();

    try {
      // 1. ユーザー名と自己紹介を更新する
      await axios.put('/api/profile', { username, bio }, {
        headers: { Authorization: `Bearer ${token}` },
        signal: controller.signal,
      });

      let newIconUrl = iconPreview;
      let newBackgroundUrl = backgroundPreview;

      // 2. アイコンが選択されていればアップロードし、スキャン中なら完了まで待つ
      if (icon) {
        setIsScanningIcon(true);
        const formData = new FormData();
        formData.append('icon', icon);
        const res = await axios.put('/api/profile/icon', formData, {
          headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'multipart/form-data' },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          await pollProfile(controller.signal);
        } else if (res.data.icon_url) {
          newIconUrl = res.data.icon_url;
        }
        setIsScanningIcon(false);
      }

      // 3. 背景画像が選択されていればアップロードし、スキャン中なら完了まで待つ
      if (background) {
        setIsScanningBackground(true);
        const formData = new FormData();
        formData.append('background', background);
        const res = await axios.put('/api/profile/background', formData, {
          headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'multipart/form-data' },
          signal: controller.signal,
        });
        if (res.data.status === 'scanning') {
          await pollProfile(controller.signal);
        } else if (res.data.background_image_url) {
          newBackgroundUrl = res.data.background_image_url;
        }
        setIsScanningBackground(false);
      }

      // 4. 認証コンテキストのユーザー情報を更新し（ヘッダーなどの表示に反映）、マイページへ遷移する
      updateUser({ username, iconUrl: newIconUrl, backgroundImageUrl: newBackgroundUrl });

      setSuccess('プロフィールを更新しました。');
      setTimeout(() => navigate('/mypage'), 1500);
    } catch (err) {
      // HTTP ステータスに応じてエラーメッセージを出し分ける（413: サイズ超過、415: 非対応形式）
      if (!axios.isCancel(err)) {
        const status = axios.isAxiosError(err) ? err.response?.status : undefined;
        if (status === 413) {
          setError('画像のサイズが大きすぎます（10MB まで）。');
        } else if (status === 415) {
          setError('対応していない画像形式です（JPEG / PNG / GIF / WebP）。');
        } else {
          setError('プロフィールの更新に失敗しました。');
        }
        setIsScanningIcon(false);
        setIsScanningBackground(false);
      }
    }
  };

  if (loading) {
    return <CircularProgress />;
  }

  return (
    <Container maxWidth="sm">
      <Typography variant="h4" component="h1" gutterBottom>
        プロフィール編集
      </Typography>
      <form onSubmit={handleSubmit}>
        {/* アイコンのプレビューと変更ボタン */}
        <Box sx={{ display: 'flex', alignItems: 'center', mb: 3 }}>
          <Avatar src={iconPreview} sx={{ width: 100, height: 100, mr: 2 }} />
          <Box>
            <Button variant="contained" component="label">
              アイコンを変更
              <input type="file" hidden accept="image/*" onChange={(e) => handleFileSelect(e, 'icon')} />
            </Button>
            {isScanningIcon && (
              <Typography variant="caption" sx={{ display: 'block', mt: 1, color: 'warning.main' }}>
                セキュリティスキャン中...
              </Typography>
            )}
          </Box>
        </Box>
        {/* 背景画像のプレビュー。領域全体が label なのでクリックでファイル選択が開く */}
        <Box sx={{ mb: 3 }}>
          <Typography gutterBottom>背景画像</Typography>
          <Box
            sx={{
              width: '100%',
              aspectRatio: '679 / 160',
              border: '1px dashed grey',
              backgroundImage: `url(${backgroundPreview})`,
              backgroundSize: 'cover',
              backgroundPosition: 'center',
              display: 'flex',
              justifyContent: 'center',
              alignItems: 'center',
              cursor: 'pointer',
              borderRadius: 1,
              color: 'text.secondary',
              position: 'relative',
            }}
            component="label"
          >
            {!backgroundPreview && "クリックして画像を選択"}
            {isScanningBackground && (
              <Box sx={{ position: 'absolute', bgcolor: 'rgba(0,0,0,0.6)', color: 'white', p: 1, borderRadius: 1 }}>
                <CircularProgress size={16} sx={{ color: 'white', mr: 1 }} />
                スキャン中...
              </Box>
            )}
            <input type="file" hidden accept="image/*" onChange={(e) => handleFileSelect(e, 'background')} />
          </Box>
        </Box>
        <TextField
          label="ユーザー名"
          fullWidth
          required
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          sx={{ mb: 2 }}
        />
        <TextField
          label="自己紹介"
          fullWidth
          multiline
          rows={4}
          value={bio}
          onChange={(e) => setBio(e.target.value)}
          sx={{ mb: 2 }}
        />
        {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
        {success && <Alert severity="success" sx={{ mb: 2 }}>{success}</Alert>}
        <Button type="submit" variant="contained" color="primary" fullWidth>
          更新する
        </Button>
      </form>

      {/* 画像トリミング用モーダル（一度でも画像を選択した後にマウントする） */}
      {imageToCrop && (
        <ImageCropperModal
          open={cropperOpen}
          onClose={() => setCropperOpen(false)}
          onSave={handleCropSave}
          imageSrc={imageToCrop}
          aspect={cropAspect}
        />
      )}
    </Container>
  );
};

export default EditProfilePage;