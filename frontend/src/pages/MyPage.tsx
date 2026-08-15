import { useState, useEffect, useRef } from 'react';
import axios from 'axios';
import { useAuth } from '../context/AuthContext';
import { Container, Typography, Box, Tab, Tabs, Card, CardContent, CardMedia, Grid, CircularProgress, Alert, CardActions, IconButton, Avatar, Paper, Button as MuiButton, Dialog, DialogContent, DialogTitle, DialogActions, useTheme } from '@mui/material';
import { Link, useNavigate } from 'react-router-dom';
import EditIcon from '@mui/icons-material/Edit';
import DeleteIcon from '@mui/icons-material/Delete';
import TuneIcon from '@mui/icons-material/Tune';
import VideocamIcon from '@mui/icons-material/Videocam';
import SportsEsportsIcon from '@mui/icons-material/SportsEsports';
import PublicIcon from '@mui/icons-material/Public';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';

interface Content {
  id: string;
  title: string;
  thumbnail_url?: string;
  type: 'video' | 'game' | 'static-site';
  uploader_id?: number;
  uploader_name?: string;
  uploader_icon?: string;
  created_at: string;
  description?: string;
  status?: string;
  entry_point_path?: string;
}

interface Profile {
  username: string;
  bio: string;
  icon_url: string;
  background_image_url: string;
}

const MyPage = () => {
  const { token, user, isLoading: isAuthLoading } = useAuth();
  const navigate = useNavigate();
  const theme = useTheme();
  const [profile, setProfile] = useState<Profile | null>(null);
  const [allMyContents, setAllMyContents] = useState<Content[]>([]);
  const [videos, setVideos] = useState<Content[]>([]);
  const [games, setGames] = useState<Content[]>([]);
  const [staticSites, setStaticSites] = useState<Content[]>([]);
  const [error, setError] = useState('');
  const [tabIndex, setTabIndex] = useState(0);
  const [isBioDialogOpen, setIsBioDialogOpen] = useState(false);
  const [isBioOverflowing, setIsBioOverflowing] = useState(false);
  const bioRef = useRef<HTMLParagraphElement>(null);
  const staticSiteDomain = import.meta.env.VITE_STATIC_SITE_DOMAIN || 'localhost';

  useEffect(() => {
    const fetchData = async () => {
      if (!token || !user || user.userID === undefined) return;
      try {
        const [profileRes, videosRes, gamesRes, staticSitesRes] = await Promise.all([
          axios.get(`/api/profile/${user.userID}`),
          axios.get('/api/my/videos', { headers: { Authorization: `Bearer ${token}` } }),
          axios.get('/api/my/games', { headers: { Authorization: `Bearer ${token}` } }),
          axios.get('/api/my/static-sites', { headers: { Authorization: `Bearer ${token}` } }),
        ]);
        setProfile(profileRes.data);
        
        const videoData: Content[] = (videosRes.data || []).map((v: any) => ({ ...v, type: 'video' }));
        const gameData: Content[] = (gamesRes.data || []).map((g: any) => ({ ...g, type: 'game' }));
        const staticSiteData: Content[] = (staticSitesRes.data || []).map((s: any) => ({ ...s, type: 'static-site' }));

        const combinedContents = [...videoData, ...gameData, ...staticSiteData].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime());
        
        const enrich = (content: Content) => ({
          ...content,
          uploader_name: profileRes.data.username || 'Unknown',
          uploader_icon: profileRes.data.icon_url || '',
          thumbnail_url: content.thumbnail_url || (content.type === 'static-site' ? '/default-static-site.png' : '/placeholder.png'),
        });

        setAllMyContents(combinedContents.map(enrich));
        setVideos(videoData.map(enrich));
        setGames(gameData.map(enrich));
        setStaticSites(staticSiteData.map(enrich));
      } catch (err) {
        setError('コンテンツの読み込みに失敗しました。');
        console.error(err);
      }
    };

    if (!isAuthLoading && user) fetchData();
  }, [isAuthLoading, user, token]);

  useEffect(() => {
    if (bioRef.current) {
      setIsBioOverflowing(bioRef.current.scrollHeight > bioRef.current.clientHeight);
    }
  }, [profile]);

  const handleTabChange = (_: React.SyntheticEvent, newValue: number) => setTabIndex(newValue);

  const handleDelete = async (type: 'video' | 'game' | 'static-site', id: string) => {
    if (window.confirm(`本当にこの${type === 'video' ? '動画' : type === 'game' ? 'ゲーム' : '静的サイト'}を削除しますか？`)) {
      try {
        const urlMap = {
          video: `/api/videos/delete/${id}`,
          game: `/api/games/${id}`,
          'static-site': `/api/static-sites/${id}`,
        };
        await axios.delete(urlMap[type], { headers: { Authorization: `Bearer ${token}` } });
        
        setAllMyContents(prev => prev.filter(c => !(c.id === id && c.type === type)));
        if (type === 'video') setVideos(prev => prev.filter(v => v.id !== id));
        if (type === 'game') setGames(prev => prev.filter(g => g.id !== id));
        if (type === 'static-site') setStaticSites(prev => prev.filter(s => s.id !== id));
      } catch (err) {
        console.error(`Failed to delete ${type}`, err);
        setError(`削除に失敗しました。`);
      }
    }
  };

  if (isAuthLoading) return <CircularProgress />;
  if (!user) return <Alert severity="error">このページを表示するにはログインが必要です。</Alert>;
  if (error) return <Alert severity="error">{error}</Alert>;

  const renderContentCards = (contentArray: Content[]) => (
    <Grid container spacing={2} justifyContent="flex-start">
      {contentArray.map((content) => {
        const isStaticSite = content.type === 'static-site';
        const linkUrl = isStaticSite 
          ? `http://${content.id}.${staticSiteDomain}:3001/${content.entry_point_path || 'index.html'}`
          : `/${content.type}s/${content.id}`;
        const CardComponent = isStaticSite ? 'a' : Link;

        return (
          <Grid item key={`${content.type}-${content.id}`}>
            <Card
              component={CardComponent}
              href={isStaticSite ? linkUrl : undefined}
              to={!isStaticSite ? linkUrl : undefined}
              target={isStaticSite ? '_blank' : undefined}
              rel={isStaticSite ? 'noopener noreferrer' : undefined}
              sx={{ 
                width: 266,
                height: 292,
                display: 'flex', 
                flexDirection: 'column', 
                textDecoration: 'none',
                boxShadow: 1,
                position: 'relative',
                '&:hover': { transform: 'translateY(-4px)', boxShadow: 6 },
                transition: 'transform 0.2s ease-in-out, box-shadow 0.2s ease-in-out',
              }}
            >
              <CardMedia 
                image={content.thumbnail_url}
                sx={{ height: 140, backgroundSize: content.type === 'game' ? 'contain' : 'cover', bgcolor: 'black' }} 
              />
              <CardContent sx={{ flexGrow: 1 }}>
                <Typography gutterBottom variant="h6" component="div" noWrap>{content.title}</Typography>
                <Box sx={{ display: 'flex', alignItems: 'center', mt: 1 }}>
                  <Avatar src={content.uploader_icon || '/default-icon.png'} sx={{ width: 24, height: 24, mr: 1, border: `1px solid ${theme.palette.background.paper}`, bgcolor: theme.palette.grey[400], boxShadow: `0 0 0 1px ${theme.palette.grey[400]}` }} />
                  <Typography variant="body2" color="text.secondary">{content.uploader_name}</Typography>
                </Box>
              </CardContent>
              <Box sx={{ position: 'absolute', top: 8, right: 8, backgroundColor: 'rgba(100, 100, 100, 0.7)', borderRadius: 1, p: '2px 4px', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                {content.type === 'video' ? <VideocamIcon sx={{ fontSize: 18, color: 'white' }} /> : content.type === 'game' ? <SportsEsportsIcon sx={{ fontSize: 18, color: 'white' }} /> : <PublicIcon sx={{ fontSize: 18, color: 'white' }} />}
              </Box>
              <CardActions sx={{ justifyContent: 'flex-end' }}>
                {content.type === 'video' && (
                  <>
                    <IconButton onClick={(e) => { e.preventDefault(); navigate(`/edit-video/${content.id}`); }} aria-label="編集"><EditIcon /></IconButton>
                    <IconButton onClick={(e) => { e.preventDefault(); handleDelete('video', content.id); }} aria-label="削除"><DeleteIcon /></IconButton>
                  </>
                )}
                {content.type === 'game' && (
                  <>
                    <IconButton onClick={(e) => { e.preventDefault(); navigate(`/adjust-game/${content.id}`); }} aria-label="調整"><TuneIcon /></IconButton>
                    <IconButton onClick={(e) => { e.preventDefault(); navigate(`/edit-game/${content.id}`); }} aria-label="編集"><EditIcon /></IconButton>
                    <IconButton onClick={(e) => { e.preventDefault(); handleDelete('game', content.id); }} aria-label="削除"><DeleteIcon /></IconButton>
                  </>
                )}
                {content.type === 'static-site' && (
                  <>
                    <IconButton onClick={(e) => { e.preventDefault(); navigate(`/static-sites/${content.id}`); }} aria-label="詳細"><OpenInNewIcon /></IconButton>
                    <IconButton onClick={(e) => { e.preventDefault(); handleDelete('static-site', content.id); }} aria-label="削除"><DeleteIcon /></IconButton>
                  </>
                )}
              </CardActions>
            </Card>
          </Grid>
        );
      })}
    </Grid>
  );

  return (
    <Container>
      {profile && (
        <Paper sx={{ mb: 4, boxShadow: 3, borderRadius: 3, overflow: 'hidden' }}>
          <Box sx={{ height: { xs: 120, sm: 160 }, width: '100%', backgroundSize: 'cover', backgroundPosition: 'center', backgroundImage: `url(${profile.background_image_url || '/default-background.jpg'})`, backgroundColor: 'grey.300', borderBottom: `2px solid ${theme.palette.grey[400]}` }} />
          <Box sx={{ px: 3, pb: 3, pt: 7, bgcolor: 'background.paper', position: 'relative' }}>
            <Avatar src={profile.icon_url || '/default-icon.png'} sx={{ width: 100, height: 100, position: 'absolute', top: -50, left: 24, border: `4px solid ${theme.palette.background.paper}`, bgcolor: theme.palette.grey[400], boxShadow: `0 0 0 2px ${theme.palette.grey[400]}` }} />
            <Typography variant="h4" component="h1" fontWeight="bold">{profile.username}</Typography>
            {profile.bio && (
              <Box sx={{ mt: 2 }}>
                <Typography ref={bioRef} variant="h6" color="text.secondary" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', lineHeight: '1.5em', maxHeight: '4.5em', overflow: 'hidden' }}>{profile.bio}</Typography>
                {isBioOverflowing && <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}><MuiButton onClick={() => setIsBioDialogOpen(true)} size="small" sx={{ mt: 0.5, textTransform: 'none', padding: 0, fontWeight: 'bold' }}>さらに表示</MuiButton></Box>}
              </Box>
            )}
          </Box>
        </Paper>
      )}
      <Dialog open={isBioDialogOpen} onClose={() => setIsBioDialogOpen(false)} maxWidth="md" fullWidth>
        <DialogTitle>{profile?.username}の自己紹介</DialogTitle>
        <DialogContent><Typography sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{profile?.bio}</Typography></DialogContent>
        <DialogActions><MuiButton onClick={() => setIsBioDialogOpen(false)}>閉じる</MuiButton></DialogActions>
      </Dialog>
      <Typography variant="h5" component="h2" gutterBottom>投稿したコンテンツ</Typography>
      <Box sx={{ borderBottom: 1, borderColor: 'divider' }}>
        <Tabs value={tabIndex} onChange={handleTabChange} aria-label="content tabs">
          <Tab label="すべて" />
          <Tab label="動画" />
          <Tab label="ゲーム" />
          <Tab label="静的サイト" />
        </Tabs>
      </Box>
      <Box sx={{ pt: 3 }}>
        {tabIndex === 0 && renderContentCards(allMyContents)}
        {tabIndex === 1 && renderContentCards(videos)}
        {tabIndex === 2 && renderContentCards(games)}
        {tabIndex === 3 && renderContentCards(staticSites)}
      </Box>
    </Container>
  );
};

export default MyPage;