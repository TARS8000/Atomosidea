import { useEffect } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';
import axios from 'axios';

const LoginSuccessPage = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const { login } = useAuth();

  useEffect(() => {
    const handleLoginSuccess = async () => {
      const params = new URLSearchParams(location.search);
      const token = params.get('token');

      if (!token) {
        console.warn("LoginSuccessPage: No token found in URL parameters.");
        navigate('/login', { replace: true });
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
        console.error("Failed to fetch user status:", error);
        // エラー発生時は安全のためログイン画面へ戻す
        navigate('/login', { replace: true });
      }
    };

    handleLoginSuccess();
  }, [location, navigate, login]);

  return <div>Loading...</div>;
};

export default LoginSuccessPage;