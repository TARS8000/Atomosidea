import { Dialog, DialogTitle, DialogContent, DialogContentText, DialogActions, Button, CircularProgress, Alert } from '@mui/material';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import { useNavigate } from 'react-router-dom';
import { useState } from 'react';

interface AccountDeletionModalProps {
  open: boolean;
  onClose: () => void;
  userProvider: string; // 'local' or 'google'
}

const AccountDeletionModal = ({ open, onClose, userProvider }: AccountDeletionModalProps) => {
  const { token, logout } = useAuth();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const handleDeleteAccount = async () => {
    if (!window.confirm('本当にアカウントを削除しますか？この操作は元に戻せません。')) {
      return;
    }

    setLoading(true);
    setError('');
    try {
      await axios.delete('/api/auth/me', {
        headers: { Authorization: `Bearer ${token}` },
      });
      alert('アカウントが正常に削除されました。');
      logout();
      navigate('/register'); // Redirect to register or login page
    } catch (err) {
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || 'アカウントの削除に失敗しました。');
      } else {
        setError('アカウントの削除に失敗しました。');
      }
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogTitle>アカウント管理</DialogTitle>
      <DialogContent>
        <DialogContentText>
          {userProvider === 'local' ? (
            <>
              このアカウントを完全に削除します。この操作は元に戻せません。
              関連する全てのデータが削除されます。
            </>
          ) : (
            <>
              このアカウントはGoogleアカウントと連携しています。
              アプリ内のデータは削除されますが、Googleアカウントとの連携は解除されません。
              連携を完全に解除したい場合は、Googleアカウントの設定ページからAtmosideaへのアクセス許可を取り消してください。
            </>
          )}
        </DialogContentText>
        {error && <Alert severity="error" sx={{ mt: 2 }}>{error}</Alert>}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={loading}>閉じる</Button>
        <Button onClick={handleDeleteAccount} color="error" disabled={loading}>
          {loading ? <CircularProgress size={24} /> : 'アカウントを削除'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};

export default AccountDeletionModal;