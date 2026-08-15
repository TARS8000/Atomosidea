import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import { Container, TextField, Button, Typography, Box, Alert, CircularProgress, Avatar } from '@mui/material';
import ImageCropperModal from '../components/ImageCropperModal';

const EditProfilePage = () => {
  const { token, user, updateUser } = useAuth();
  const navigate = useNavigate();
  
  const [username, setUsername] = useState('');
  const [bio, setBio] = useState('');
  
  const [icon, setIcon] = useState<File | null>(null);
  const [iconPreview, setIconPreview] = useState('');
  const [background, setBackground] = useState<File | null>(null);
  const [backgroundPreview, setBackgroundPreview] = useState('');
  
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const [cropperOpen, setCropperOpen] = useState(false);
  const [imageToCrop, setImageToCrop] = useState('');
  const [cropAspect, setCropAspect] = useState(1);
  const [editingImageType, setEditingImageType] = useState<'icon' | 'background' | null>(null);

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

  const handleFileSelect = (e: React.ChangeEvent<HTMLInputElement>, type: 'icon' | 'background') => {
    if (e.target.files && e.target.files[0]) {
      const file = e.target.files[0];
      const reader = new FileReader();
      reader.onload = () => {
        setImageToCrop(reader.result as string);
        setEditingImageType(type);
        setCropAspect(type === 'icon' ? 1 : 679 / 160); // Set aspect ratio for background
        setCropperOpen(true);
      };
      reader.readAsDataURL(file);
    }
    e.target.value = '';
  };

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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess('');

    try {
      await axios.put('/api/profile', { username, bio }, {
        headers: { Authorization: `Bearer ${token}` },
      });

      let newIconUrl = iconPreview;
      if (icon) {
        const formData = new FormData();
        formData.append('icon', icon);
        const res = await axios.put('/api/profile/icon', formData, {
          headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'multipart/form-data' },
        });
        newIconUrl = res.data.icon_url;
      }

      if (background) {
        const formData = new FormData();
        formData.append('background', background);
        await axios.put('/api/profile/background', formData, {
          headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'multipart/form-data' },
        });
      }

      updateUser({ username, iconUrl: newIconUrl });

      setSuccess('プロフィールを更新しました。');
      setTimeout(() => navigate('/mypage'), 1500);
    } catch (err) {
      setError('プロフィールの更新に失敗しました。');
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
        <Box sx={{ display: 'flex', alignItems: 'center', mb: 3 }}>
          <Avatar src={iconPreview} sx={{ width: 100, height: 100, mr: 2 }} />
          <Button variant="contained" component="label">
            アイコンを変更
            <input type="file" hidden accept="image/*" onChange={(e) => handleFileSelect(e, 'icon')} />
          </Button>
        </Box>
        <Box sx={{ mb: 3 }}>
          <Typography gutterBottom>背景画像</Typography>
          <Box
            sx={{
              width: '100%',
              aspectRatio: '679 / 160', // Set aspect ratio to 679:160
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
            }}
            component="label"
          >
            {!backgroundPreview && "クリックして画像を選択"}
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