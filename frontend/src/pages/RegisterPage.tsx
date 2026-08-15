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

const RegisterPage = () => {
  const [adminCode, setAdminCode] = useState('');
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const navigate = useNavigate();

  const handleGoogleLogin = () => {
    window.location.href = '/api/auth/google/login';
  };

  const handleAdminSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setSuccess('');
    try {
      await axios.post('/api/auth/register', { adminCode });
      setSuccess('管理者アカウントの登録リクエストが成功しました。ログインページにリダイレクトします...');
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
                id="adminCode"
                label="管理者コード"
                name="adminCode"
                autoFocus
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