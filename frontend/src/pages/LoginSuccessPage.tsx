import { useEffect, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';
import axios from 'axios';

// Google ログイン後のコールバック画面。
// auth-service は JWT を URL に載せず、60 秒・1 回限りのコード（?code=...）を渡す。
// ここでコードを POST /api/auth/oauth/exchange でトークンに交換する。
const LoginSuccessPage = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const { login } = useAuth();
  // コードは 1 回しか使えないため、StrictMode などによる 2 回目の実行では交換しない
  const handledRef = useRef(false);

  useEffect(() => {
    if (handledRef.current) return;
    handledRef.current = true;

    const handleLoginSuccess = async () => {
      const params = new URLSearchParams(location.search);
      const code = params.get('code');

      // コードを履歴・アドレスバーに残さない
      window.history.replaceState(window.history.state, '', location.pathname);

      if (!code) {
        console.warn('LoginSuccessPage: No code found in URL parameters.');
        navigate('/login', { replace: true });
        return;
      }

      let token: string;
      try {
        const exchangeRes = await axios.post('/api/auth/oauth/exchange', { code });
        token = exchangeRes.data.token;
        if (!token) throw new Error('No token in exchange response');
      } catch (error) {
        console.error('Failed to exchange login code:', error);
        navigate('/login?error=code_exchange_failed', { replace: true });
        return;
      }

      // 1. AuthContext & localStorage にトークンを保存
      login(token);

      try {
        // 2. トークンを明示的に Authorization ヘッダーにセットしてステータスを取得
        const response = await axios.get('/api/profile/status', {
          headers: {
            Authorization: `Bearer ${token}`,
          },
        });

        // 3. ユーザーのステータスに応じて遷移先を変更
        if (response.data.status === 'pending') {
          navigate('/edit-profile', { replace: true });
        } else {
          navigate('/mypage', { replace: true });
        }
      } catch (error) {
        console.error('Failed to fetch user status:', error);
        // エラー発生時は安全のためログイン画面へ戻す
        navigate('/login', { replace: true });
      }
    };

    handleLoginSuccess();
  }, [location, navigate, login]);

  return <div>Loading...</div>;
};

export default LoginSuccessPage;
