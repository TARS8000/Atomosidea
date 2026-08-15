import { BrowserRouter as Router, Routes, Route } from 'react-router-dom';
import { AuthProvider } from './context/AuthContext';
import Header from './components/Header';
import HomePage from './pages/HomePage';
import UploadPage from './pages/UploadPage';
import LoginPage from './pages/LoginPage';
import RegisterPage from './pages/RegisterPage'; // Corrected import path
import VideoDetailPage from './pages/VideoDetailPage';
import LoginSuccessPage from './pages/LoginSuccessPage';
import UploadGamePage from './pages/UploadGamePage';
import GameDetailPage from './pages/GameDetailPage';
import AdjustGamePage from './pages/AdjustGamePage';
import EditGamePage from './pages/EditGamePage';
import EditVideoPage from './pages/EditVideoPage';
import MyPage from './pages/MyPage';
import EditProfilePage from './pages/EditProfilePage';
import UploadStaticSitePage from './pages/UploadStaticSitePage';
import StaticSiteDetailPage from './pages/StaticSiteDetailPage';
import EditStaticSitePage from './pages/EditStaticSitePage'; // Import the new page
import { Container, CssBaseline, ThemeProvider } from '@mui/material';
import theme from './theme';

function App() {
  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <Router>
        <AuthProvider>
          <Header />
          <Container component="main" sx={{ pt: 12, mb: 4 }}>
            <Routes>
              <Route path="/" element={<HomePage />} />
              <Route path="/upload" element={<UploadPage />} />
              <Route path="/upload-game" element={<UploadGamePage />} />
              <Route path="/upload-static-site" element={<UploadStaticSitePage />} />
              <Route path="/login" element={<LoginPage />} />
              <Route path="/register" element={<RegisterPage />} />
              <Route path="/videos/:id" element={<VideoDetailPage />} />
              <Route path="/games/:id" element={<GameDetailPage />} />
              <Route path="/static-sites/:id" element={<StaticSiteDetailPage />} />
              <Route path="/adjust-game/:id" element={<AdjustGamePage />} />
              <Route path="/edit-game/:id" element={<EditGamePage />} />
              <Route path="/edit-video/:id" element={<EditVideoPage />} />
              <Route path="/edit-static-site/:id" element={<EditStaticSitePage />} /> {/* Add this line */}
              <Route path="/login/success" element={<LoginSuccessPage />} />
              <Route path="/mypage" element={<MyPage />} />
              <Route path="/edit-profile" element={<EditProfilePage />} />
            </Routes>
          </Container>
        </AuthProvider>
      </Router>
    </ThemeProvider>
  );
}

export default App;