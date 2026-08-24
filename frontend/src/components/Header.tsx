import { useState } from 'react';
import { AppBar, Toolbar, Typography, Button, Box, Avatar, Menu, MenuItem, ListItemIcon, ListItemText, useTheme } from '@mui/material';
import { Link as RouterLink, useNavigate } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';
import UploadIcon from '@mui/icons-material/Upload';
import VideocamIcon from '@mui/icons-material/Videocam';
import SportsEsportsIcon from '@mui/icons-material/SportsEsports';
import PublicIcon from '@mui/icons-material/Public'; // Import PublicIcon
import AccountDeletionModal from './AccountDeletionModal';
import axios from 'axios';

const Header = () => {
  const { isAuthenticated, user, logout, token } = useAuth();
  const navigate = useNavigate();
  const theme = useTheme();
  const [userMenuAnchorEl, setUserMenuAnchorEl] = useState<null | HTMLElement>(null);
  const [uploadMenuAnchorEl, setUploadMenuAnchorEl] = useState<null | HTMLElement>(null);
  const [isAccountModalOpen, setIsAccountModalOpen] = useState(false);

  const handleUserMenu = (event: React.MouseEvent<HTMLElement>) => {
    setUserMenuAnchorEl(event.currentTarget);
  };

  const handleUploadMenu = (event: React.MouseEvent<HTMLElement>) => {
    setUploadMenuAnchorEl(event.currentTarget);
  };

  const handleClose = () => {
    setUserMenuAnchorEl(null);
    setUploadMenuAnchorEl(null);
  };

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

  const handleOpenAccountModal = () => {
    handleClose();
    setIsAccountModalOpen(true);
  };

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
        {isAuthenticated && user ? (
          <Box sx={{ display: 'flex', alignItems: 'center' }}>
            <Button
              variant="outlined"
              onClick={handleUploadMenu}
              startIcon={<UploadIcon />}
              sx={{ mr: 2 }}
            >
              アップロード
            </Button>
            <Menu
              anchorEl={uploadMenuAnchorEl}
              open={Boolean(uploadMenuAnchorEl)}
              onClose={handleClose}
            >
              <MenuItem component={RouterLink} to="/upload" onClick={handleClose}>
                <ListItemIcon>
                  <VideocamIcon fontSize="small" />
                </ListItemIcon>
                <ListItemText>動画をアップロード</ListItemText>
              </MenuItem>
              <MenuItem component={RouterLink} to="/upload-game" onClick={handleClose}>
                <ListItemIcon>
                  <SportsEsportsIcon fontSize="small" />
                </ListItemIcon>
                <ListItemText>ゲームをアップロード</ListItemText>
              </MenuItem>
              <MenuItem component={RouterLink} to="/upload-static-site" onClick={handleClose}>
                <ListItemIcon>
                  <PublicIcon fontSize="small" />
                </ListItemIcon>
                <ListItemText>静的サイトをアップロード</ListItemText>
              </MenuItem>
            </Menu>

            <Button component={RouterLink} to="/mypage" sx={{ color: 'text.primary', mr: 2 }}>マイページ</Button>
            
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