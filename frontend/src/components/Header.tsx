import { useState } from 'react';
import { AppBar, Toolbar, Typography, Button, Box, Avatar, Menu, MenuItem, ListItemText, useTheme } from '@mui/material';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';
import UploadIcon from '@mui/icons-material/Upload';
import GroupIcon from '@mui/icons-material/Group';
import AccountDeletionModal from './AccountDeletionModal';
import axios from 'axios';

/**
 * 画面上部に固定表示するヘッダー。ログイン状態に応じて、ナビゲーションとユーザーメニュー、
 * またはログイン・新規登録ボタンを表示する。
 */
const Header = () => {
  const { isAuthenticated, user, logout, token } = useAuth();
  const navigate = useNavigate();
  const theme = useTheme();
  const [userMenuAnchorEl, setUserMenuAnchorEl] = useState<null | HTMLElement>(null);
  const [isAccountModalOpen, setIsAccountModalOpen] = useState(false);

  // ユーザー名部分のクリックでメニューを開く（クリックした要素をメニューの表示位置の基準にする）。
  const handleUserMenu = (event: React.MouseEvent<HTMLElement>) => {
    setUserMenuAnchorEl(event.currentTarget);
  };

  // ユーザーメニューを閉じる。
  const handleClose = () => {
    setUserMenuAnchorEl(null);
  };

  // ログアウト API を呼び（失敗しても続行）、ローカルの認証状態を破棄してトップへ遷移する。
  // 注意: AuthContext の logout() もログアウト API を呼ぶため API が 2 回呼ばれ、logout() 内の navigate('/login') が後から実行される。
  const handleLogout = async () => {
    handleClose();
    try {
      await axios.post('/api/auth/logout', {}, {
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch (error) {
      console.error("Logout failed", error);
    } finally {
      logout();
      navigate('/');
    }
  };

  // メニューを閉じて、アカウント管理ダイアログを開く。
  const handleOpenAccountModal = () => {
    handleClose();
    setIsAccountModalOpen(true);
  };

  // アカウント管理ダイアログを閉じる。
  const handleCloseAccountModal = () => {
    setIsAccountModalOpen(false);
  };

  return (
    <AppBar
      position="fixed"
      sx={{
        backgroundColor: 'rgba(255, 255, 255, 0.8)',
        backdropFilter: 'blur(8px)',
        boxShadow: 'none',
        borderBottom: (theme) => `1px solid ${theme.palette.divider}`,
      }}
      elevation={0}
    >
      <Toolbar>
        <Typography
          variant="h6"
          component={RouterLink}
          to="/"
          sx={{
            flexGrow: 1,
            textDecoration: 'none',
            color: 'text.primary',
            fontWeight: 700,
          }}
        >
          Atmosidea
        </Typography>
        {/* ログイン済みならナビゲーションとユーザーメニュー、未ログインならログイン・新規登録ボタンを表示する */}
        {isAuthenticated && user ? (
          <Box sx={{ display: 'flex', alignItems: 'center' }}>
            <Button
              component={RouterLink}
              to="/upload"
              variant="outlined"
              startIcon={<UploadIcon />}
              sx={{ mr: 2 }}
            >
              アップロード
            </Button>
            <Button component={RouterLink} to="/teams" startIcon={<GroupIcon />} sx={{ mr: 2, color: 'text.primary' }}>
              チーム
            </Button>

            <Button component={RouterLink} to="/mypage" sx={{ color: 'text.primary', mr: 2 }}>マイページ</Button>
            
            {/* アイコンとユーザー名。クリックでユーザーメニューを開く */}
            <Box
              onClick={handleUserMenu}
              sx={{
                display: 'flex',
                alignItems: 'center',
                cursor: 'pointer',
                border: '1px solid',
                borderColor: 'divider',
                borderRadius: 1,
                p: 0.5,
                '&:hover': {
                  bgcolor: 'action.hover'
                }
              }}
            >
              <Avatar 
                src={user.iconUrl || '/default-icon.png'} 
                sx={{ 
                  width: 32, 
                  height: 32, 
                  bgcolor: theme.palette.grey[400],
                  border: `1px solid ${theme.palette.background.paper}`,
                  boxShadow: `0 0 0 1px ${theme.palette.grey[400]}`,
                }} 
              />
              <Typography sx={{ color: 'text.primary', ml: 1, mr: 0.5 }}>{user.username}</Typography>
            </Box>
            {/* ユーザーメニュー（マイページ・プロフィール編集・アカウント管理・ログアウト） */}
            <Menu
              id="menu-appbar"
              anchorEl={userMenuAnchorEl}
              anchorOrigin={{
                vertical: 'bottom',
                horizontal: 'right',
              }}
              keepMounted
              transformOrigin={{
                vertical: 'top',
                horizontal: 'right',
              }}
              open={Boolean(userMenuAnchorEl)}
              onClose={handleClose}
              sx={{ mt: 1.5 }}
            >
              <MenuItem component={RouterLink} to="/mypage" onClick={handleClose}>マイページ</MenuItem>
              <MenuItem component={RouterLink} to="/edit-profile" onClick={handleClose}>
                <ListItemText>プロフィールを編集</ListItemText>
              </MenuItem>
              <MenuItem onClick={handleOpenAccountModal}>アカウント管理</MenuItem>
              <MenuItem onClick={handleLogout}>ログアウト</MenuItem>
            </Menu>
          </Box>
        ) : (
          <Box>
            <Button component={RouterLink} to="/login" sx={{ color: 'text.primary' }}>ログイン</Button>
            <Button component={RouterLink} to="/register" sx={{ color: 'text.primary' }}>新規登録</Button>
          </Box>
        )}
      </Toolbar>
      {/* ユーザー情報がある場合のみダイアログを配置する。provider が不明なら空文字を渡す */}
      {user && (
        <AccountDeletionModal
          open={isAccountModalOpen}
          onClose={handleCloseAccountModal}
          userProvider={user.provider ?? ''}
        />
      )}
    </AppBar>
  );
};

export default Header;