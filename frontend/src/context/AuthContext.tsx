import { createContext, useState, useContext, useEffect, ReactNode, useCallback } from 'react';
import { jwtDecode } from 'jwt-decode';
import axios from 'axios';
import { useNavigate } from 'react-router-dom';

// ★ UUID v7 化に伴い userID を string に変更
export interface User {
  userID: string;
  username: string;
  isAdmin: boolean;
  iconUrl?: string;
  provider?: string;
  status?: string;
  bio?: string;
  backgroundImageUrl?: string;
}

interface AuthContextType {
  isAuthenticated: boolean;
  isLoading: boolean;
  user: User | null;
  token: string | null;
  login: (token: string) => void;
  logout: () => void;
  updateUser: (newUserData: Partial<User>) => void;
}

const AuthContext = createContext<AuthContextType | undefined>(undefined);

// ★ Axios Interceptor: リクエスト送信時に確実に Authorization ヘッダーを付与
axios.interceptors.request.use(
    (config) => {
      const token = localStorage.getItem('token');
      if (token) {
        if (config.headers && typeof config.headers.set === 'function') {
          config.headers.set('Authorization', `Bearer ${token}`);
        } else {
          config.headers = config.headers || {};
          config.headers['Authorization'] = `Bearer ${token}`;
        }
      } else {
        console.warn('[Axios Interceptor] No token found in localStorage for:', config.url);
      }
      return config;
    },
    (error) => {
      return Promise.reject(error);
    }
);

// JWTデコード用の型定義
interface DecodedToken {
  sub?: string;
  userID?: string;
  user_id?: string;
  exp: number;
  isAdmin?: boolean;
  is_admin?: boolean;
}

const isTokenExpired = (token: string): boolean => {
  try {
    const decoded: DecodedToken = jwtDecode(token);
    return Date.now() >= decoded.exp * 1000;
  } catch {
    return true;
  }
};

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [token, setToken] = useState<string | null>(localStorage.getItem('token'));
  const [user, setUser] = useState<User | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const navigate = useNavigate();

  // サーバーに問い合わせずにローカルの認証状態だけを破棄する
  const clearSession = useCallback(() => {
    localStorage.removeItem('token');
    delete axios.defaults.headers.common['Authorization'];
    setToken(null);
    setUser(null);
  }, []);

  const logout = useCallback(async () => {
    const currentToken = localStorage.getItem('token');
    if (currentToken && !isTokenExpired(currentToken)) {
      try {
        await axios.post('/api/auth/logout', {});
      } catch (error) {
        console.error('Logout API call failed:', error);
      }
    }
    clearSession();
    navigate('/login', { replace: true });
  }, [navigate, clearSession]);

  useEffect(() => {
    const loadUser = async () => {
      const storedToken = localStorage.getItem('token');
      console.log('[AuthProvider] Stored token check:', storedToken ? 'EXISTS' : 'NULL');

      if (!storedToken) {
        setUser(null);
        setIsLoading(false);
        return;
      }

      try {
        // デフォルトヘッダーの同期セット
        axios.defaults.headers.common['Authorization'] = `Bearer ${storedToken}`;

        const decoded: DecodedToken = jwtDecode(storedToken);

        // 期限切れチェック（期限切れトークンでは logout API が 401 になるため、ローカルの破棄のみ行う）
        if (isTokenExpired(storedToken)) {
          console.warn('[AuthProvider] Token expired');
          clearSession();
          navigate('/login', { replace: true });
          setIsLoading(false);
          return;
        }

        // JWTからのuserID取得 (sub, userID, user_id の優先順位でパース)
        const currentUserID = decoded.sub || decoded.userID || decoded.user_id || '';

        // プロフィール（/api/profile/me）と、認証方式 provider を持つ auth-db の情報（/api/auth/me）を並行取得する。
        // /api/auth/me は 401（トークン無効）のときだけ失敗扱いにし、それ以外の失敗では provider 不明のまま続行する。
        const [profileRes, authMe] = await Promise.all([
          axios.get('/api/profile/me'),
          axios
            .get<{ provider?: string }>('/api/auth/me')
            .then((res) => res.data)
            .catch((err: unknown) => {
              if (axios.isAxiosError(err) && err.response?.status === 401) throw err;
              console.warn('[AuthProvider] Failed to load auth info (provider):', err);
              return null;
            }),
        ]);
        const profile = profileRes.data;

        const currentUser: User = {
          userID: profile.id || currentUserID,
          username: profile.username,
          isAdmin: !!(decoded.isAdmin || decoded.is_admin),
          iconUrl: profile.icon_url,
          provider: authMe?.provider,
          status: profile.status,
          bio: profile.bio,
          backgroundImageUrl: profile.background_image_url,
        };

        setUser(currentUser);

        // 初回プロフィール設定が完了していない場合は編集画面へ
        if (currentUser.status === 'pending') {
          navigate('/edit-profile', { replace: true });
        }
      } catch (error) {
        // トークン自体が無効（401）な場合のみセッションを破棄する。
        // プロフィール取得の一時的な失敗等ではログイン状態を維持し、JWT の情報でユーザーを構成する
        if (axios.isAxiosError(error) && error.response?.status === 401) {
          console.error('[AuthProvider] Token rejected by server:', error);
          clearSession();
        } else {
          console.error('[AuthProvider] Failed to load profile:', error);
          try {
            const decoded: DecodedToken & { username?: string } = jwtDecode(storedToken);
            setUser({
              userID: decoded.sub || decoded.userID || decoded.user_id || '',
              username: decoded.username || '',
              isAdmin: !!(decoded.isAdmin || decoded.is_admin),
            });
          } catch {
            clearSession();
          }
        }
      } finally {
        setIsLoading(false);
      }
    };

    loadUser();
  }, [token, clearSession, navigate]);

  const login = useCallback((newToken: string) => {
    console.log('[AuthProvider] login() called with token');
    localStorage.setItem('token', newToken);
    axios.defaults.headers.common['Authorization'] = `Bearer ${newToken}`;
    setToken(newToken);
  }, []);

  const updateUser = useCallback((newUserData: Partial<User>) => {
    setUser((prevUser) => (prevUser ? { ...prevUser, ...newUserData } : null));
  }, []);

  if (isLoading) {
    return <div>Loading...</div>;
  }

  return (
      <AuthContext.Provider value={{ isAuthenticated: !!token, isLoading, user, token, login, logout, updateUser }}>
        {children}
      </AuthContext.Provider>
  );
};

// eslint-disable-next-line react-refresh/only-export-components
export const useAuth = () => {
  const context = useContext(AuthContext);
  if (context === undefined) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
};