import { Dialog, DialogTitle, DialogContent, DialogContentText, DialogActions, Button, CircularProgress, Alert } from '@mui/material';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import { useNavigate } from 'react-router-dom';
import { useState } from 'react';

/** AccountDeletionModal の props。 */
interface AccountDeletionModalProps {
  open: boolean;
  onClose: () => void;
  userProvider: string; // 'local' または 'google'
}

/**
 * アカウント管理（削除）ダイアログ。認証方式に応じた説明を表示し、DELETE /api/auth/me でアカウントを削除する。
 */
const AccountDeletionModal = ({ open, onClose, userProvider }: AccountDeletionModalProps) => {
  const { token, logout } = useAuth();
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  // 確認後にアカウント削除 API を呼び、成功したらログアウトして登録ページへ遷移する。
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
      // logout() は非同期で、ログアウト API の完了後に '/login' へ遷移する。
      // そのため直後の navigate('/register') は、後から '/login' への遷移で上書きされる。
      logout();
      navigate('/register'); // 登録ページ（またはログインページ）へリダイレクトする
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
          {/* ローカルアカウントか、それ以外（Google 連携・provider 不明）かで説明文を切り替える */}
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