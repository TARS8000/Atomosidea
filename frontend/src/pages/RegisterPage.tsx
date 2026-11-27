import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import {
  Container,
  Button,
  Typography,
  Box,
  Alert,
  Divider,
  Accordion,
  AccordionSummary,
  AccordionDetails,
  TextField
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';

/**
 * 新規登録画面。一般ユーザーは Google で登録し、管理者は管理者コードを入力して
 * メールアドレスとパスワードで登録する（ローカル登録は管理者アカウントのみ）。
 */
const RegisterPage = () => {
  const [adminCode, setAdminCode] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const navigate = useNavigate();

  // Google での登録はログインと同じ OAuth 開始エンドポイントへ遷移する（初回ログイン時にアカウントが作られる）。
  const handleGoogleLogin = () => {
    window.location.href = '/api/auth/google/login';
  };

  // 管理者コード付きで管理者アカウントを登録し、成功したら 2 秒後にログイン画面へ遷移する。
  // 管理者コードが一致しない場合などはサーバーのエラーメッセージを表示する。
  const handleAdminSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess('');
    try {
      await axios.post('/api/auth/register', { email, password, adminCode });
      setSuccess('管理者アカウントを作成しました。ログインページにリダイレクトします...');
      setTimeout(() => navigate('/login'), 2000);
    } catch (err) {
      if (axios.isAxiosError(err) && err.response) {
        setError(err.response.data.error || '登録に失敗しました。');
      } else {
        setError('不明なエラーが発生しました。');
      }
    }
  };

  return (
    <Container maxWidth="xs">
      <Box sx={{ mt: 8, display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
        <Typography variant="h4" component="h1" gutterBottom>
          新規登録
        </Typography>

        <Button
          fullWidth
          variant="outlined"
          onClick={handleGoogleLogin}
          sx={{ mt: 2, mb: 2 }}
        >
          Googleで登録
        </Button>

        <Divider sx={{ width: '100%', my: 2 }}>または</Divider>

        <Accordion sx={{ width: '100%' }}>
          <AccordionSummary
            expandIcon={<ExpandMoreIcon />}
            aria-controls="admin-register-content"
            id="admin-register-header"
          >
            <Typography>管理者として登録</Typography>
          </AccordionSummary>
          <AccordionDetails>
            <Box component="form" onSubmit={handleAdminSubmit} sx={{ mt: 1 }}>
              <TextField
                margin="normal"
                required
                fullWidth
                id="email"
                label="メールアドレス"
                name="email"
                type="email"
                autoComplete="email"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
              <TextField
                margin="normal"
                required
                fullWidth
                id="password"
                label="パスワード（8文字以上）"
                name="password"
                type="password"
                autoComplete="new-password"
                inputProps={{ minLength: 8 }}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              {/* 管理者コード。バックエンドの設定値と照合され、一致しなければ登録は拒否される */}
              <TextField
                margin="normal"
                required
                fullWidth
                id="adminCode"
                label="管理者コード"
                name="adminCode"
                type="password"
                value={adminCode}
                onChange={(e) => setAdminCode(e.target.value)}
              />
              {error && <Alert severity="error" sx={{ mt: 2, mb: 1 }}>{error}</Alert>}
              {success && <Alert severity="success" sx={{ mb: 2 }}>{success}</Alert>}
              <Button
                type="submit"
                fullWidth
                variant="contained"
                sx={{ mt: 3, mb: 2 }}
              >
                管理者登録
              </Button>
            </Box>
          </AccordionDetails>
        </Accordion>
      </Box>
    </Container>
  );
};

export default RegisterPage;