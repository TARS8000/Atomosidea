import { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
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

// Google ログインのコールバックから ?error=<code> で戻ってきたときの表示文言
const oauthErrorMessages: Record<string, string> = {
  account_disabled: 'このアカウントは削除済みのため、ログインできません。',
  state_mismatch: 'ログイン処理の有効期限が切れたか、不正なリクエストです。もう一度お試しください。',
  code_exchange_failed: 'ログイン処理の有効期限が切れました。もう一度お試しください。',
};

/**
 * ログイン画面。通常は Google ログインを使い、管理者向けにメールアドレスとパスワードのログイン欄も表示する。
 * Google ログイン失敗時に付く ?error=<code> を読み取ってエラーを表示する。
 */
const LoginPage = () => {
  const [searchParams] = useSearchParams();
  const oauthError = searchParams.get('error');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  // 未知のエラーコードは汎用メッセージにする
  const oauthErrorMessage = oauthError
    ? oauthErrorMessages[oauthError] || 'Google ログインに失敗しました。'
    : '';
  const [error, setError] = useState('');
  const navigate = useNavigate();
  const { login } = useAuth();

  // Google ログインは、バックエンドの OAuth 開始エンドポイントへページごと遷移して始める。
  const handleGoogleLogin = () => {
    window.location.href = '/api/auth/google/login';
  };

  // メールアドレスとパスワードでログインし、受け取った JWT を保存してマイページへ遷移する。
  // 403 は無効化（削除済み）アカウントを表す。
  const handleAdminSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    try {
      const response = await axios.post('/api/auth/login', { email, password });
      login(response.data.token);
      navigate('/mypage'); // マイページへリダイレクトする
    } catch (err) {
      if (axios.isAxiosError(err) && err.response?.status === 403) {
        setError('このアカウントは無効化されているため、ログインできません。');
      } else {
        setError('管理者ログインに失敗しました。');
      }
    }
  };

  return (
    <Container maxWidth="xs">
      <Box sx={{ mt: 8, display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
        <Typography variant="h4" component="h1" gutterBottom>
          ログイン
        </Typography>

        {oauthErrorMessage && (
          <Alert severity="error" sx={{ width: '100%', mt: 2 }}>{oauthErrorMessage}</Alert>
        )}

        <Button
          fullWidth
          variant="outlined"
          onClick={handleGoogleLogin}
          sx={{ mt: 2, mb: 2 }}
        >
          Googleでログイン
        </Button>

        <Divider sx={{ width: '100%', my: 2 }}>または</Divider>

        <Accordion sx={{ width: '100%' }}>
          <AccordionSummary
            expandIcon={<ExpandMoreIcon />}
            aria-controls="admin-login-content"
            id="admin-login-header"
          >
            <Typography>管理者としてログイン</Typography>
          </AccordionSummary>
          <AccordionDetails>
            <Box component="form" onSubmit={handleAdminSubmit} sx={{ mt: 1 }}>
              <TextField
                margin="normal"
                required
                fullWidth
                id="email"
                label="管理者メールアドレス"
                name="email"
                autoComplete="email"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
              <TextField
                margin="normal"
                required
                fullWidth
                name="password"
                label="管理者パスワード"
                type="password"
                id="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              {error && <Alert severity="error" sx={{ mt: 2, mb: 1 }}>{error}</Alert>}
              <Button
                type="submit"
                fullWidth
                variant="contained"
                sx={{ mt: 3, mb: 2 }}
              >
                管理者ログイン
              </Button>
            </Box>
          </AccordionDetails>
        </Accordion>
      </Box>
    </Container>
  );
};

export default LoginPage;